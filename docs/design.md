# obscli — Design Document

`obscli` is a CLI tool for declarative management of [Open Build Service (OBS)](https://openbuildservice.org/)
projects, used by the Kubernetes project to manage the `isv:kubernetes` project tree on
[build.opensuse.org](https://build.opensuse.org/).

The desired state of OBS projects (subprojects, their metadata, packages, and package metadata) is
declared in YAML configuration files stored in a Git repository. Running `obscli` reconciles the
live OBS state against the declared state: it fetches the current state, computes the differences,
and either reports them (dry-run, the default) or applies them (`--confirm`).

The tool is modeled after [peribolos](https://github.com/kubernetes-sigs/prow/tree/main/cmd/peribolos)
(declarative GitHub org management) and [aquayman](https://github.com/kubermatic-labs/aquayman)
(declarative Quay.io org management), and is designed to run in CI (Prow):

- **Presubmit**: `obscli validate` checks configuration offline (no credentials).
- **Presubmit** (unprivileged OBS account): `obscli apply` without `--confirm` prints the changes
  that would be made.
- **Postsubmit** (privileged OBS account): `obscli apply --confirm` applies the changes.

## Background: the `isv:kubernetes` project tree

The tool must handle the existing structure of `isv:kubernetes`, which is a multi-level hierarchy
of subprojects with four distinct project roles:

1. **Root umbrella** — `isv:kubernetes`: holds the maintainer/bugowner lists, inherited by all
   subprojects; all build-related flags (`build`, `publish`, `debuginfo`, `useforbuild`) disabled;
   no repositories.
2. **Grouping umbrellas** — e.g. `isv:kubernetes:core`, `isv:kubernetes:core:stable`,
   `isv:kubernetes:core:prerelease`: essentially empty projects used only to group their
   subprojects; no repositories.
3. **Release projects** — e.g. `isv:kubernetes:core:stable:v1.34`,
   `isv:kubernetes:core:prerelease:v1.38`: `kind="maintenance_release"`, build disabled, `rpm` and
   `deb` repositories (paths and architectures are configured in the YAML). Packages in these
   projects are **timestamped release copies** (e.g. `kubeadm.20260923195931`) created by the OBS
   release mechanism. These copies are machine-managed and must never be managed individually
   by obscli; deleting their project also deletes them (see "Release copies" below).
4. **Builder projects** — e.g. `isv:kubernetes:core:stable:v1.34:build`: `rpm` and `deb`
   repositories, each carrying a
   `<releasetarget project="…:v1.34" repository="…" trigger="manual"/>` pointing at the parent
   release project. Unlike release projects, packages here are not timestamped — these are the
   real, editable packages: `cri-tools`, `kubeadm`, `kubectl`, `kubelet`, `kubernetes-cni`.

There is also `isv:kubernetes:core:shared` / `isv:kubernetes:core:shared:build` for shared tooling,
following the same release/builder pattern, and an `isv:kubernetes:addons` subtree (e.g.
`isv:kubernetes:addons:cri-o:…`) which is one level deeper but otherwise follows the very same
four roles. Package `_meta` in the tree is currently empty (title/description only). Project
`_config` (prjconf) is currently empty everywhere.

A new Kubernetes minor version means creating one release project + one builder project pair. Old
version projects are effectively frozen once the version goes EOL.

## Scope

### In scope

- **Project existence and project `_meta`**: title, description, url, kind, people (users with
  roles), groups (role assignment only), flags, repositories (paths, architectures, release
  targets).
- **Package existence and package `_meta`**: title, description, url, people, flags.
- **Pruning** of unmanaged projects/packages, governed by a configurable per-project policy.
- Commands: `apply`, `validate`, `export`, `new-version`.
- **`new-version` scaffolding**: copy the previous version's YAML files and rewrite the version
  string to bootstrap a new Kubernetes minor version. Templating at creation time, not reconcile
  time.

### Out of scope permanently (owned by krel, the Kubernetes release tooling)

- **Release triggering** (releasing builder project results into release projects).
- **Package sources/files** (spec files, tarballs, `_service` files). obscli never reads or writes
  package file contents; its package scope ends at "the package exists and its `_meta` matches".

### Deferred (not in v1, but the design must not preclude them)

- **prjconf (`_config`) management.** v1 does not read, compare, or write prjconf at all. When
  added later, it will use sidecar files (e.g. `v1.34.build.prjconf`) referenced from the YAML via
  a `prjconfFile` field, with "field absent = unmanaged" semantics. prjconf is a line-based DSL
  and must not be embedded in YAML block scalars.
- **Group creation and group membership management.** Creating groups on build.opensuse.org
  requires OBS instance admin rights and is done out-of-band. The config schema accepts group role
  assignments on projects (see below), but obscli does not manage the groups themselves.

## Explicitly rejected designs

These were considered and rejected; do not reintroduce them:

- **Templating/generator layer** (define a "channel" template, instantiate per version). Rejected
  because templates drift over time (e.g. from v1.40 the deb base distro changes while older
  versions stay frozen), which forces conditionals, template forks, or override sprawl. The config
  is raw and explicit: one YAML document per project. Bulk-edit convenience is sacrificed
  deliberately; old versions are frozen and rarely edited.
- **Terraform-style plan/apply with a persisted plan artifact.** Rejected as unnecessary and
  CI-unfriendly. Dry-run output (logs of would-be changes) is the plan; a subsequent run with
  `--confirm` executes.
- **peribolos-style `--fix-*` subsystem gating flags.** One run reconciles everything in scope.
  Scoping is done by config path (see `apply` below), not by subsystem flags.
- **Raw XML blobs in YAML** (for package meta or anything else). Everything compared must be
  structured so diffs are semantic, not textual.

## CLI

Single binary `obscli` with subcommands (use `spf13/cobra`):

```
obscli apply       --config <dir-or-file> [--confirm] [--allow-deletions] [--required-maintainers <users>]
obscli validate    --config <dir-or-file> [--required-maintainers <users>]
obscli export      --project <obs-project> [--recursive] [--output <dir>] [--with-packages]
obscli new-version --config <dir> --from <version> --to <version>
```

### `apply`

- Loads all `*.yaml` files under `--config` (recursively if a directory; exactly one file if a
  file path). The set of projects declared in the loaded files is the **reconciliation scope**.
- Fetches the current state of each declared project from OBS, computes differences, and logs
  every change in aquayman style (`+` create, `-` delete, `✎` update), including field-level
  detail for updates. Package actions identify the full `<project>/<package>`, even when the
  project's own metadata has not changed.
- **Dry-run by default.** Without `--confirm`, no mutating API call is made; the log output *is*
  the change proposal. With `--confirm`, the same code path executes the mutations.
- Reconciliation creates missing projects (parents before children) and prepares missing
  repositories before applying metadata in dependency order. Obsolete repositories remain until
  consumers have been updated. Cyclic project dependencies are rejected before any OBS requests.
  Cleanup runs last: obsolete repositories, then packages, then projects in reverse hierarchy
  order. See "Reconciliation semantics" for details.
- **Top level names are never managed.** A name without a colon, such as `isv` or `home`, is a
  namespace that whole unrelated trees live under, not a project obscli may own. Declaring one is
  a validation error and `export --project isv` is refused, so the shallowest manageable project
  always has a parent: `isv:kubernetes`, `home:someuser`.
- **No gaps in the declared tree.** Hierarchy is encoded in the name: dropping the last
  colon-separated segment of `isv:kubernetes:core:stable` gives its parent
  `isv:kubernetes:core`, and repeating that gives its ancestors `isv:kubernetes` and `isv`. An
  **undeclared project sitting between two declared ones is a validation error** — undeclared
  meaning no YAML file describes it, whether or not it exists in OBS — because obscli
  would have to create a project it neither owns nor describes in order to reach the one below
  it. Declaring `isv:kubernetes` and `isv:kubernetes:core:stable` without `isv:kubernetes:core`
  is refused; the fix is to declare the missing project.
  - The check is confined to the declared projects, which are the only tree obscli knows about.
    Walking up from a project, the first **declared** ancestor anchors it and anything undeclared
    passed on the way there is a gap. A project that finds no declared ancestor at all is a
    **root**, and nothing above a root is checked.
  - This makes the check independent of what OBS happens to contain: it is an offline check run
    by `validate` and by `apply`, and no project outside the config is ever even looked up. A
    scoped run on a single deep file declares one project with no declared ancestor, so there is
    nothing to check, while the same project in a full run must have its whole chain of parents
    declared.
- Scoped runs (`--config` pointing at a subdirectory or single file) reconcile only the declared
  projects and never consider undeclared sibling/child projects for pruning.
- **Project-level pruning** ("project exists in OBS but has no YAML") is governed by
  `subprojects.policy` on the *declared* project it sits below. Since an undeclared project has
  no file of its own, the policy that governs it is the one on its **nearest declared ancestor** —
  the exact project-level counterpart of `packages.policy`. This gives per-subtree control (prune
  under `core:stable`, ignore under `addons`) and keeps a run self-limiting: obscli only ever
  enumerates below projects the configuration actually declares, so a run scoped to
  `isv:kubernetes:core:stable` can never reach `isv:kubernetes:addons`. Undeclared projects are
  deleted children-first. Enumeration is skipped entirely for a subtree whose policy is `ignore`.
- **Deletions are additionally gated by `--allow-deletions`.** `--confirm` alone never deletes
  projects or packages — deletion candidates (per prune policy) are only reported. Actual deletion
  requires `--confirm --allow-deletions`, intended for manual runs; CI never sets it.

### Safety checks

Run by both `validate` and `apply` (they are offline, on the desired state):

- **Minimum owners**: every declared project must have at least one effective maintainer —
  declared directly or inherited from an ancestor project declared in the config.
- **No gaps in the declared tree**: an undeclared project between two declared ones is refused.
  See "Project hierarchy and ownership" above.
- **Repository references**: paths and release targets pointing to a declared project must name
  a repository declared in that project. A repository cannot reference itself, but it may
  reference a different repository in the same project. References to undeclared projects are
  checked only for valid syntax, since their repositories cannot be verified offline.
- **Required maintainers** (`--required-maintainers`, comma-separated users, default empty): each
  listed user must be an effective maintainer of every declared project. CI sets
  `--required-maintainers=k8s-release-bot` to guarantee the bot can never be locked out.

### `validate`

- Offline only: no network, no credentials.
- Checks: YAML syntax, schema conformance, required fields, valid enum values (roles, flag
  actions, trigger values, policies), duplicate project `name` across all loaded files, duplicate
  package names within a project, repository paths and release targets (including existence of
  repositories on declared projects and rejection of repository self-references), well-formed
  OBS project/package/user names, no gaps in the declared project tree. Project and package
  name validation is shared with the release-sdk client, including special OBS package names
  such as `_patchinfo` and `_product:example`.

### `export`

- Bootstraps YAML config from live OBS state for one-time adoption. Export does not refresh or
  migrate an established configuration tree.
- `--project` names the root project to export; `--recursive` includes all subprojects
  (name-prefix `<project>:`); `--with-packages` includes the package list and package meta in the
  output.
- Writes one YAML file per project into `--output` following the recommended layout convention
  (see below). The output directory must be new or empty. A non-empty directory, including one
  containing only hidden files or empty subdirectories, is rejected before any OBS requests.
  A missing directory is created when writing the export. Without `--output`, prints to stdout.
- Treats OBS responses as untrusted: every returned project/package identity must match the
  requested resource, and recursive results must be strict descendants of the requested root.
- Plans the complete output layout before writing. Export fails without creating files when two
  project names map to the same portable path, when a file conflicts with a required directory,
  or when any destination would escape `--output`.
- Export must round-trip with `apply`: applying a freshly exported config against the same OBS
  state must produce zero changes.
- When exporting release projects, timestamped release copies (see "Release copies" below) are
  excluded from the package list.
- Export emits the most compact config form: scalar flag shorthand when a flag has a single
  unscoped rule, name-only package items when the package meta is empty, omitted keys when empty.
  The goal is that exported files look hand-written. For example:

  ```yaml
  flags:
    build: disable          # not: build: [disable]
  packages:
    managed:
      - name: kubelet       # not: {name: kubelet, title: ""}
  # no empty keys such as bugowners: [] or url: ""
  ```

### `new-version`

- Offline scaffolding helper to bootstrap a new Kubernetes minor version: finds all YAML files
  under `--config` whose project `name` contains the `--from` version segment (e.g. `v1.39`),
  copies them, and rewrites every occurrence of the old version with the new one (project names,
  titles, descriptions, release targets, file names).
- Example: `obscli new-version --config config/ --from v1.39 --to v1.40` creates
  `core/stable/v1.40.yaml`, `core/stable/v1.40.build.yaml`, `core/prerelease/v1.40.yaml`, … next
  to their `v1.39` counterparts.
- Generated paths are always relative to `--config`. Existing targets are never replaced, and a
  failed multi-file write removes files created earlier by that invocation.
- Purely local file generation; no OBS access. The result is reviewed and applied like any other
  config change.

## Configuration schema

One YAML document per OBS project. Example showing every supported field (builder project, the
most complex case):

```yaml
name: isv:kubernetes:core:stable:v1.34:build     # required; authoritative project identity
kind: maintenance_release                        # optional; omitted = standard project
title: Kubernetes v1.34 (Stable Builder)
description: |
  This is a project used to build packages for Kubernetes v1.34. Don't use this
  project/repository directly, instead use "isv:kubernetes:core:stable:v1.34" project.
url: https://kubernetes.io                       # optional

# People: role-keyed lists of OBS usernames. The key is the role.
maintainers: [k8s-release-bot, xmudrii]
bugowners: []
# Also supported: reviewers, readers, downloaders (the full OBS role set).

# Groups: role assignment only (obscli never creates groups or manages their membership).
groups:
  maintainers: []                                # same role keys as for users

# Flags: build, publish, useforbuild, debuginfo (the four the OBS UI exposes).
# Value is either a scalar shorthand or a list of rules (see "Flags" below).
flags:
  build: disable

repositories:
  - name: rpm
    architectures: [x86_64, aarch64, ppc64le, s390x]
    paths:                                       # ordered; objects, never strings
      - project: SUSE:SLE-15-SP5:GA
        repository: standard
    releaseTargets:                              # optional list; only on builder projects
      - project: isv:kubernetes:core:stable:v1.34
        repository: rpm
        trigger: manual                          # manual | maintenance | obsgendiff (or omitted)
  - name: deb
    architectures: [x86_64, aarch64, ppc64le, s390x]
    paths:
      - project: Ubuntu:20.04
        repository: universe
      - project: Ubuntu:debbuild
        repository: Ubuntu_20.04
    releaseTargets:
      - project: isv:kubernetes:core:stable:v1.34
        repository: deb
        trigger: manual

packages:
  policy: report                                 # ignore | report | prune
  managed:
    - name: kubelet                              # name only: desired meta is empty
    - name: kubeadm
    - name: kubectl
    - name: kubernetes-cni
    - name: cri-tools                            # with meta fields: existence + managed meta
      title: CRI Tools
      description: CLI and validation tools for Kubelet Container Runtime Interface
      url: https://github.com/kubernetes-sigs/cri-tools
      maintainers: []                            # same people schema as projects
      flags: {}                                  # same flags schema as projects

# Subprojects: how to treat projects that exist in OBS below this one but are
# declared in no YAML file. Same policy values as packages.
subprojects:
  policy: report                                 # ignore | report | prune
```

A minimal umbrella project:

```yaml
name: isv:kubernetes
title: Kubernetes
description: Official Kubernetes package repositories sponsored by OpenBuildService
url: https://kubernetes.io
maintainers: [cpanato, jrrickard, justaugustus, k8s-release-bot, puerco, sagrunert, verolop, xmudrii]
bugowners:   [cpanato, jrrickard, justaugustus, k8s-release-bot, puerco, sagrunert, verolop, xmudrii]
flags:
  build: disable
  publish: disable
  debuginfo: disable
  useforbuild: disable
```

### `name` is authoritative; layout is convention

- The `name:` field is the sole source of project identity. **The directory layout carries no
  semantics**: obscli recursively loads `*.yaml` under `--config` and builds the project set from
  the `name` fields. Files can be organized freely.
- The only structural rule enforced: no duplicate `name` across loaded files (validation error).
- Recommended (unenforced) repo convention, which `export` produces: flat files mirroring the
  hierarchy —

  ```
  config/
    kubernetes.yaml                 # isv:kubernetes
    core.yaml                       # isv:kubernetes:core
    core/
      stable.yaml                   # isv:kubernetes:core:stable
      stable/
        v1.34.yaml                  # isv:kubernetes:core:stable:v1.34
        v1.34.build.yaml            # isv:kubernetes:core:stable:v1.34:build
      prerelease.yaml
      prerelease/
        ...
  ```

  The rule `export` applies: **a project becomes a directory only when it has a grandchild.**
  Otherwise the remaining name segments are joined with a dot into the file name. That is what
  keeps a builder project next to the project it builds for (`v1.34.yaml` and
  `v1.34.build.yaml`) instead of burying it in a `v1.34/` directory holding a single file. The
  layout is generated by `export` and is never read back for meaning. If valid project names
  would make that flattening ambiguous, including on a case-insensitive file system, export
  reports the conflicting projects instead of overwriting either one.

### Flags

OBS project/package flags are ordered rule lists: each flag element (`<build>`, `<publish>`, …)
contains `<enable/>` / `<disable/>` rules, each optionally scoped by `repository=` and/or `arch=`
attributes, with most-specific-match-wins evaluation.

obscli models exactly the four flags the OBS web UI exposes: `build`, `publish`, `useforbuild`
and `debuginfo`. OBS itself supports more — `binarydownload` (also a scoped rule list), plus the
*simple* flags `lock` and `sourceaccess` (projects and packages) and `access` (projects only),
which take a bare `<enable/>`/`<disable/>` with no scoping. These are deliberately **not**
modeled: they are not reachable from the UI, and none of them is used anywhere in the
`isv:kubernetes` tree. Per "unmodeled XML is dropped", an apply would clear them; `apply` reports
such a removal as an ordinary `binarydownload:` change line rather than doing it silently. Note
that `access` and `sourceaccess` control project visibility, so if the tree ever starts using
them they must be added to the schema first.

The YAML supports two forms:

```yaml
# Scalar shorthand — sugar for a single unscoped rule (the overwhelmingly common case):
flags:
  build: disable

# Full rule-list form — faithful 1:1 mapping to the XML:
flags:
  build:
    - disable                                    # unscoped rule as scalar list item
    - enable: {repository: rpm}                  # scoped rule as single-key map
    - disable: {repository: rpm, arch: s390x}
```

Internally both forms decode to the same `[]FlagRule{Action, Repository, Arch}` representation via
a custom `UnmarshalYAML` (using `yaml.v3` node kinds: scalar → single rule; sequence → items that
are either scalars or single-key maps with optional `repository`/`arch`). The reconciler, differ,
and XML layer only ever see `[]FlagRule`. Export emits the scalar shorthand when a flag has
exactly one unscoped rule, the list form otherwise.

### Packages

- `packages.policy` controls treatment of packages that exist in OBS but are not listed in
  `managed`:
  - `ignore` — never report, never touch (used for release projects, where package contents are
    machine-managed release copies).
  - `report` — log unmanaged packages as a warning/diff, but never delete (safe default).
  - `prune` — delete unmanaged packages (requires `--confirm --allow-deletions`).
- Default policy when `packages` is omitted: `report` with an empty `managed` list.
- `managed` items are always objects with a required `name`; all other fields are optional. The
  declared fields are the complete desired `_meta`: an item with only `name` means the desired
  meta is the minimal one (package header with empty `<title/>` and `<description/>`). If someone
  hand-edits a package's meta on OBS, obscli reconciles it back to the declared state.

### Subprojects

- `subprojects.policy` is the same three-valued policy, applied to projects instead of packages:
  it controls the treatment of projects that exist in OBS below this one but are declared in no
  YAML file. Default when `subprojects` is omitted: `report`.
- The policy is resolved per undeclared project through its **nearest declared ancestor**, so a
  subtree can opt out of its parent's policy. With `isv:kubernetes` set to `prune` and
  `isv:kubernetes:addons` set to `ignore`, an undeclared `isv:kubernetes:addons:foo` is left
  alone while an undeclared `isv:kubernetes:obsolete` is deleted.
- Pruning a project removes all its packages, including release copies, and all its subprojects.
  Subprojects are deleted before their parents. Package-level policies do not preserve the
  contents of a deleted project; deletion still requires `--confirm --allow-deletions`.
- obscli enumerates OBS only below the topmost declared project whose policy is not `ignore`;
  the search matches the whole subtree, so one call per such root is enough, and a fully
  `ignore`d configuration makes no enumeration call at all.
- This is why the policy lives in the config rather than on the command line: it is a property of
  the tree being managed, not of the run, and it keeps `apply` scoped to what the configuration
  actually declares.

### Release copies

Packages whose names match `<basename>` followed by one or more `.<14-digit-timestamp>` suffixes
(e.g. `kubeadm.20260923195931`, `kubernetes-cni.20260424110236.20260424140455`) are
OBS release-mechanism copies. obscli must always exclude them from package-level reconciliation
and export, regardless of `packages.policy`. They are never individually created, compared,
reported as unmanaged, or deleted.

This protection does not prevent whole-project deletion. When a project is pruned, all its
packages, including release copies, and all its subprojects are removed.

## Reconciliation semantics

- **Validate live identities before writing.** Read all declared projects before any mutation.
  Every successful response must contain metadata whose project name matches the requested
  name; missing metadata or a mismatched name stops both dry and confirmed runs. Only an OBS
  not-found response is treated as a missing project that may be created.
- **Normalize before diff.** Both the desired config and the live `_meta` XML are converted into
  the same canonical internal model before comparison. Normalization must eliminate false diffs
  from XML element order, attribute order, people/group ordering, and OBS-injected defaults.
  People are normalized to `{subject, kind: user|group, role}` tuples; sets are compared
  order-insensitively. Repository order, path order within a repository, architecture order
  within a repository and flag rule order are meaningful in OBS and are compared as ordered
  lists. Title, description and URL are compared trimmed, so that a YAML block scalar (which
  always ends in a newline) does not diff forever against what OBS stores.
- **Unmodeled XML is dropped.** obscli models the elements listed in the configuration schema and
  nothing else. Since a PUT replaces the whole document, any element obscli does not model is
  removed from the project on the next apply. This is deliberate: the config is the complete
  desired state. The only exception is prjconf, which lives at a different endpoint and is never
  touched.
- **Only send deltas.** Updates PUT the full new `_meta` document (the OBS API is
  whole-document), but a PUT happens only if the normalized models differ.
- **The config is the complete desired state.** Omitted fields translate to their empty XML
  elements, not "unmanaged": e.g. a maintainer present in OBS but absent from the YAML is removed,
  and a package declared with only `name` is reconciled to the minimal meta (package header with
  empty `<title/>` and `<description/>`). The sole exception in v1 is prjconf, which is never read
  or written.
- **Preparation:** first create missing projects with minimal meta (header with empty `<title/>`
  and `<description/>`), parents before children. Then add missing repositories without their
  new paths or release targets, preserving existing modeled metadata, repositories and
  references. This establishes every declared repository identity before references are applied,
  including same-project release targets that appear later in the repository list.
- **Metadata application:** apply desired metadata in dependency order, with referenced projects
  before their dependants. Retain obsolete repositories and their references temporarily.
  Desired repository order is preserved. References to undeclared projects do not expand
  reconciliation scope and must already resolve in OBS. The existing restriction on cyclic
  project dependencies remains: dry runs and confirmed runs reject them before any OBS requests.
- **Cleanup:** after all project and package creates and updates succeed, detach references
  between obsolete repositories so they cannot prevent each other's removal, then remove those
  repositories. For example, replacing a referenced repository first creates the new target,
  then redirects consumers, then removes the old target. OBS rejects removal if an out-of-scope
  consumer still references the old repository; obscli does not force that removal. Repository
  cleanup remains part of metadata reconciliation, not the `--allow-deletions` gate for whole
  packages and projects. Package deletions follow repository cleanup, then project deletions,
  children before parents. Any error stops the run before later phases.
- **Logging and counts:** log each project's intended final diff before its first mutation.
  Preparation and cleanup may require multiple metadata writes, but a project counts as created
  or updated only once. An existing project counts as updated after its first successful write,
  even if a later phase fails. Dry runs track the same intermediate metadata without writing it.
- **Dry-run mechanism:** the OBS client carries a `Dry bool`; every mutating method logs and
  returns without performing the HTTP call when `Dry` is true. The reconciler is written once and
  behaves identically in both modes (belt-and-braces: reconcile logic only calls mutators after
  logging the intended change).

## OBS API

Base URL: `https://api.opensuse.org` (flag-overridable, e.g. `--api-url`). Authentication: HTTP
Basic auth from environment variables `OBS_USERNAME` and `OBS_PASSWORD` (fits Prow secret
mounting). Read-only operations in dry-run still require credentials except where the public
mirror endpoints suffice; keep it simple and always authenticate.

Endpoints used:

| Operation | Endpoint |
|---|---|
| Read project meta | `GET /source/<project>/_meta` |
| Write project meta | `PUT /source/<project>/_meta` |
| Delete project | `DELETE /source/<project>` |
| List packages | `GET /source/<project>` |
| Read package meta | `GET /source/<project>/<package>/_meta` |
| Write package meta (also creates package) | `PUT /source/<project>/<package>/_meta` |
| Delete package | `DELETE /source/<project>/<package>` |
| List subprojects (for export/prune) | `GET /search/project/id?match=starts-with(@name,'<prefix>')` (requires auth) |

All payloads are XML; model them with `encoding/xml` structs mirroring the OBS schema:

```xml
<project name="..." kind="maintenance_release">
  <title>...</title>
  <description>...</description>
  <url>...</url>
  <person userid="..." role="maintainer"/>
  <group groupid="..." role="maintainer"/>
  <build><disable/><enable repository="rpm"/></build>
  <publish><disable/></publish>
  <repository name="rpm">
    <releasetarget project="..." repository="rpm" trigger="manual"/>
    <path project="SUSE:SLE-15-SP5:GA" repository="standard"/>
    <arch>x86_64</arch>
  </repository>
</project>
```

```xml
<package name="..." project="...">
  <title>...</title>
  <description>...</description>
  <url>...</url>
  <person userid="..." role="maintainer"/>
  <build><disable/></build>
</package>
```

## Architecture

Go module `sigs.k8s.io/obscli`. Package layout (aquayman-style; avoid peribolos'
everything-in-main):

```
main.go / cmd/           thin cobra wiring: apply, validate, export, new-version
pkg/config/              YAML schema types, custom unmarshaler (flags),
                         load (file or recursive dir), save (for export), offline validation
pkg/sync/                reconciliation: normalization, diffing, ordered apply, logging
pkg/export/              live OBS state → config types → YAML files
```

The OBS API client does **not** live in obscli. It lives in `sigs.k8s.io/release-sdk/obs`, which
this project owns: XML types, GET/PUT/DELETE wrappers, a `Dry` flag and release-copy detection.
Keeping the client in the SDK lets krel and obscli share one OBS client instead of growing a
second one.

Implementation guidelines (adopted from peribolos/aquayman):

- **Small per-consumer interfaces**: each reconcile function takes a narrow interface (2–4
  methods) rather than the whole client, so unit tests use tiny fakes. Example:
  `type projectMetaClient interface { GetProjectMeta(ctx, name) (*obs.Project, error); PutProjectMeta(ctx, name, *obs.Project) error }`.
- **Log format** (aquayman-style, human-scannable in CI logs):
  `+` create, `-` delete, `✎` update, nested detail lines for field-level changes. Dry-run and
  real runs produce the same log lines; the final line states either "run again with --confirm"
  or "changes applied".
- **Same code path for dry and real runs**; no separate plan data structure.
- Unit tests for: config load/validate (both flag forms, duplicate names), normalization/diff
  (false-diff avoidance from XML ordering), reconcile ordering,
  release-copy exclusion, export round-trip (export → load → diff against source == empty).
- Command tests execute the Cobra commands against a local HTTP server using the real SDK
  client. They cover mutation gates, metadata payloads and dependency ordering, credential and
  API errors, partial-run summaries, and offline validation and version scaffolding.

## Prior art reference

- **aquayman** (`kubermatic-labs/aquayman`): package layout, `Dry` client flag, `+/-/✎` logging,
  export mode, two-tier validation.
- **peribolos** (`kubernetes-sigs/prow/cmd/peribolos`): narrow per-consumer client interfaces,
  normalize-before-diff, config-as-complete-desired-state semantics.

## Resources

- OBS documentation: <https://openbuildservice.org/help/>
- OBS API documentation: <https://api.opensuse.org/apidocs/index>
- OBS instance: <https://build.opensuse.org/> (API at <https://api.opensuse.org>)
- The managed project tree: <https://build.opensuse.org/project/show/isv:kubernetes>
- Live examples of the structures described in this document (public, no auth needed):
  - Project meta: <https://api.opensuse.org/public/source/isv:kubernetes:core:stable:v1.34:build/_meta>
  - Package list: <https://api.opensuse.org/public/source/isv:kubernetes:core:stable:v1.34:build>
  - Package meta: <https://api.opensuse.org/public/source/isv:kubernetes:core:stable:v1.34:build/kubelet/_meta>
- peribolos: <https://github.com/kubernetes-sigs/prow/tree/main/cmd/peribolos>
- aquayman: <https://github.com/kubermatic-labs/aquayman>
