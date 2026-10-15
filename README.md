# obscli

Command-line interface for managing OpenBuildService (OBS) assets

Manage OBS project and package metadata with YAML configuration.

## Installation

From a source checkout, using the Go version required by [go.mod](go.mod):

```sh
go build -o obscli .
./obscli --help
```

The examples below assume `obscli` is on your `PATH`.

## Authentication

```sh
export OBS_USERNAME='alice'
export OBS_PASSWORD='your-obs-password'
```

`apply` and `export` require credentials. The default API is
`https://api.opensuse.org`; use `--api-url` to select another instance.

## Configuration

Each YAML file describes one project. For example, `config/tools.yaml`
(replace `alice` with your OBS username):

```yaml
name: home:alice:tools
title: Command-line tools
maintainers:
  - alice
flags:
  build: disable
repositories:
  - name: rpm
    architectures:
      - x86_64
      - aarch64
packages:
  policy: report
  managed:
    - name: hello-world
      title: Hello World
subprojects:
  policy: report
```

`--config` accepts a file or a directory of `.yaml` and `.yml` files, searched
recursively. Project identity comes from `name`, not the file path.

**Configuration replaces managed metadata rather than merging with it.** Omitted
fields can be removed from OBS when an update is applied.

## Usage

Validate offline, preview changes, then apply:

```sh
obscli validate --config config/
obscli apply --config config/
obscli apply --config config/ --confirm
```

`apply` never writes without `--confirm`. For undeclared packages and subprojects,
the policies are `report` (default), `ignore`, and `prune`. With `prune`, enable
package and project deletions explicitly:

```sh
obscli apply --config config/ --allow-deletions --confirm
```

Project deletion includes all packages and descendants. Repository removal is a
metadata change and does not require `--allow-deletions`.

## Export existing projects

Generate configuration from an existing project:

```sh
obscli export \
  --project home:alice:tools \
  --recursive \
  --with-packages \
  --output exported-config/
```

`--recursive` includes subprojects; `--with-packages` includes package metadata.
The output directory must be new or empty. Without `--output`, YAML is printed to
standard output.

## New Kubernetes minor version

For configurations with versioned project names, copy an existing version:

```sh
obscli new-version --config config/ --from v1.34 --to v1.35
```

This creates local files with updated version references in their names and
contents. It does not modify OBS.

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack](https://kubernetes.slack.com/messages/sig-release)
- [Mailing List](https://groups.google.com/g/kubernetes-sig-release)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).

[owners]: https://git.k8s.io/community/contributors/guide/owners.md
[Creative Commons 4.0]: https://git.k8s.io/website/LICENSE
