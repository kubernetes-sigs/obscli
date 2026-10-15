/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sync

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/go-cmp/cmp"

	"sigs.k8s.io/release-sdk/obs"
)

// maxValuePreview is the length a value is shortened to before it is logged,
// so that a long description does not flood the output.
const maxValuePreview = 60

// noneValue is shown in place of an absent value.
const noneValue = "(none)"

// ProjectChanges describes, field by field, what applying the desired meta
// would change on the live project. It returns nothing when the two documents
// are equal. Both documents are normalized first.
func ProjectChanges(desired, live *obs.Project) []string {
	desired, live = NormalizeProject(desired), NormalizeProject(live)
	if cmp.Equal(desired, live) {
		return nil
	}

	var changes []string

	changes = append(changes, scalarChange("kind", string(live.Kind), string(desired.Kind))...)
	changes = append(changes, scalarChange("title", live.Title, desired.Title)...)
	changes = append(changes, scalarChange("description", live.Description, desired.Description)...)
	changes = append(changes, scalarChange("url", live.URL, desired.URL)...)
	changes = append(changes, personChanges(live.Persons, desired.Persons)...)
	changes = append(changes, groupChanges(live.Groups, desired.Groups)...)
	changes = append(changes, flagChanges("build", live.Build, desired.Build)...)
	changes = append(changes, flagChanges("publish", live.Publish, desired.Publish)...)
	changes = append(changes, flagChanges("useforbuild", live.UseForBuild, desired.UseForBuild)...)
	changes = append(changes, flagChanges("debuginfo", live.DebugInfo, desired.DebugInfo)...)
	changes = append(changes, flagChanges("binarydownload", live.BinaryDownload, desired.BinaryDownload)...)
	changes = append(changes, repositoryChanges(live.Repositories, desired.Repositories)...)

	if len(changes) == 0 {
		// The documents differ in a field that is not reported individually.
		changes = append(changes, "meta changed")
	}

	return changes
}

// PackageChanges describes what applying the desired meta would change on the
// live package.
func PackageChanges(desired, live *obs.Package) []string {
	desired, live = NormalizePackage(desired), NormalizePackage(live)
	if cmp.Equal(desired, live) {
		return nil
	}

	var changes []string

	changes = append(changes, scalarChange("title", live.Title, desired.Title)...)
	changes = append(changes, scalarChange("description", live.Description, desired.Description)...)
	changes = append(changes, scalarChange("url", live.URL, desired.URL)...)
	changes = append(changes, scalarChange("releasename", live.ReleaseName, desired.ReleaseName)...)
	changes = append(changes, scalarChange("bcntsynctag", live.BcntSyncTag, desired.BcntSyncTag)...)
	changes = append(changes, develChange(live.Devel, desired.Devel)...)
	changes = append(changes, personChanges(live.Persons, desired.Persons)...)
	changes = append(changes, groupChanges(live.Groups, desired.Groups)...)
	changes = append(changes, flagChanges("build", live.Build, desired.Build)...)
	changes = append(changes, flagChanges("publish", live.Publish, desired.Publish)...)
	changes = append(changes, flagChanges("useforbuild", live.UseForBuild, desired.UseForBuild)...)
	changes = append(changes, flagChanges("debuginfo", live.DebugInfo, desired.DebugInfo)...)
	changes = append(changes, flagChanges("binarydownload", live.BinaryDownload, desired.BinaryDownload)...)

	if len(changes) == 0 {
		changes = append(changes, "meta changed")
	}

	return changes
}

// scalarChange reports a single changed text field.
func scalarChange(field, before, after string) []string {
	if before == after {
		return nil
	}

	return []string{fmt.Sprintf("%s: %s → %s", field, preview(before), preview(after))}
}

// develChange reports a changed devel reference.
func develChange(before, after *obs.Devel) []string {
	format := func(devel *obs.Devel) string {
		if devel == nil {
			return noneValue
		}

		if devel.Package == "" {
			return devel.Project
		}

		return devel.Project + "/" + devel.Package
	}

	if format(before) == format(after) {
		return nil
	}

	return []string{fmt.Sprintf("devel: %s → %s", format(before), format(after))}
}

// personChanges reports added and removed user role assignments, grouped by
// role so that the output reads like the configuration.
func personChanges(before, after []obs.Person) []string {
	beforeByRole := make(map[obs.Role][]string, len(before))
	for _, person := range before {
		beforeByRole[person.Role] = append(beforeByRole[person.Role], person.UserID)
	}

	afterByRole := make(map[obs.Role][]string, len(after))
	for _, person := range after {
		afterByRole[person.Role] = append(afterByRole[person.Role], person.UserID)
	}

	return subjectChanges("", beforeByRole, afterByRole)
}

// groupChanges reports added and removed group role assignments.
func groupChanges(before, after []obs.Group) []string {
	beforeByRole := make(map[obs.Role][]string, len(before))
	for _, group := range before {
		beforeByRole[group.Role] = append(beforeByRole[group.Role], group.GroupID)
	}

	afterByRole := make(map[obs.Role][]string, len(after))
	for _, group := range after {
		afterByRole[group.Role] = append(afterByRole[group.Role], group.GroupID)
	}

	return subjectChanges("group ", beforeByRole, afterByRole)
}

// subjectChanges renders the per role difference of two role assignments.
func subjectChanges(prefix string, before, after map[obs.Role][]string) []string {
	roles := make([]obs.Role, 0, len(before)+len(after))

	for role := range before {
		roles = append(roles, role)
	}

	for role := range after {
		if _, ok := before[role]; !ok {
			roles = append(roles, role)
		}
	}

	sort.Slice(roles, func(i, j int) bool {
		if rank, otherRank := roleRank(roles[i]), roleRank(roles[j]); rank != otherRank {
			return rank < otherRank
		}

		return roles[i] < roles[j]
	})

	var changes []string

	for _, role := range roles {
		added, removed := difference(before[role], after[role])
		if len(added) == 0 && len(removed) == 0 {
			continue
		}

		var parts []string

		for _, subject := range added {
			parts = append(parts, "+"+subject)
		}

		for _, subject := range removed {
			parts = append(parts, "-"+subject)
		}

		changes = append(changes, fmt.Sprintf("%s%ss: %s", prefix, role, strings.Join(parts, " ")))
	}

	return changes
}

// flagChanges reports a changed flag as its rendered before and after state,
// because the rules of a flag only make sense as an ordered whole.
func flagChanges(name string, before, after obs.Flag) []string {
	if cmp.Equal(before, after) {
		return nil
	}

	return []string{fmt.Sprintf("%s: %s → %s", name, renderFlag(before), renderFlag(after))}
}

// renderFlag renders a flag as a compact, single line rule list.
func renderFlag(flag obs.Flag) string {
	if len(flag) == 0 {
		return "(unset)"
	}

	rules := make([]string, 0, len(flag))

	for _, rule := range flag {
		var scope []string

		if rule.Repository != "" {
			scope = append(scope, "repository="+rule.Repository)
		}

		if rule.Arch != "" {
			scope = append(scope, "arch="+rule.Arch)
		}

		if len(scope) == 0 {
			rules = append(rules, string(rule.Action))

			continue
		}

		rules = append(rules, fmt.Sprintf("%s(%s)", rule.Action, strings.Join(scope, ",")))
	}

	return "[" + strings.Join(rules, " ") + "]"
}

// repositoryChanges reports added, removed and modified repositories. The
// order of repositories is meaningful, so a pure reordering is reported too.
func repositoryChanges(before, after []obs.Repository) []string {
	beforeByName := make(map[string]*obs.Repository, len(before))
	beforeNames := make([]string, 0, len(before))

	for i := range before {
		beforeByName[before[i].Name] = &before[i]

		beforeNames = append(beforeNames, before[i].Name)
	}

	afterByName := make(map[string]*obs.Repository, len(after))
	afterNames := make([]string, 0, len(after))

	for i := range after {
		afterByName[after[i].Name] = &after[i]

		afterNames = append(afterNames, after[i].Name)
	}

	added, removed := difference(beforeNames, afterNames)

	var changes []string

	for _, name := range added {
		changes = append(changes, "+repository "+name)
	}

	for _, name := range removed {
		changes = append(changes, "-repository "+name)
	}

	for _, name := range afterNames {
		liveRepository, ok := beforeByName[name]
		if !ok {
			continue
		}

		changes = append(changes, repositoryDetail(name, liveRepository, afterByName[name])...)
	}

	if len(added) == 0 && len(removed) == 0 && !cmp.Equal(beforeNames, afterNames) {
		changes = append(changes, fmt.Sprintf(
			"repository order: %s → %s",
			strings.Join(beforeNames, ","), strings.Join(afterNames, ","),
		))
	}

	return changes
}

// repositoryDetail reports the changed fields of a single repository.
func repositoryDetail(name string, before, after *obs.Repository) []string {
	var changes []string

	prefix := "repository " + name + ": "

	for _, change := range scalarChange("rebuild", before.Rebuild, after.Rebuild) {
		changes = append(changes, prefix+change)
	}

	for _, change := range scalarChange("block", before.Block, after.Block) {
		changes = append(changes, prefix+change)
	}

	for _, change := range scalarChange("linkedbuild", before.LinkedBuild, after.LinkedBuild) {
		changes = append(changes, prefix+change)
	}

	if !cmp.Equal(before.Architectures, after.Architectures) {
		changes = append(changes, fmt.Sprintf("%sarchitectures: %s → %s", prefix,
			preview(strings.Join(before.Architectures, ",")),
			preview(strings.Join(after.Architectures, ",")),
		))
	}

	if !cmp.Equal(before.Paths, after.Paths) {
		changes = append(changes, fmt.Sprintf("%spaths: %s → %s", prefix,
			preview(renderPaths(before.Paths)), preview(renderPaths(after.Paths)),
		))
	}

	if !cmp.Equal(before.ReleaseTargets, after.ReleaseTargets) {
		changes = append(changes, fmt.Sprintf("%srelease targets: %s → %s", prefix,
			preview(renderReleaseTargets(before.ReleaseTargets)),
			preview(renderReleaseTargets(after.ReleaseTargets)),
		))
	}

	return changes
}

// renderPaths renders the build paths of a repository as a single line.
func renderPaths(paths []obs.RepositoryPath) string {
	if len(paths) == 0 {
		return noneValue
	}

	rendered := make([]string, 0, len(paths))
	for _, path := range paths {
		rendered = append(rendered, path.Project+"/"+path.Repository)
	}

	return strings.Join(rendered, " ")
}

// renderReleaseTargets renders the release targets of a repository as a single
// line.
func renderReleaseTargets(targets []obs.ReleaseTarget) string {
	if len(targets) == 0 {
		return noneValue
	}

	rendered := make([]string, 0, len(targets))

	for _, target := range targets {
		entry := target.Project + "/" + target.Repository
		if target.Trigger != "" {
			entry += "(" + string(target.Trigger) + ")"
		}

		rendered = append(rendered, entry)
	}

	return strings.Join(rendered, " ")
}

// difference returns the entries that after adds and the ones it removes,
// both in a stable order.
func difference(before, after []string) (added, removed []string) {
	beforeSet := make(map[string]struct{}, len(before))
	for _, entry := range before {
		beforeSet[entry] = struct{}{}
	}

	afterSet := make(map[string]struct{}, len(after))
	for _, entry := range after {
		afterSet[entry] = struct{}{}
	}

	for _, entry := range after {
		if _, ok := beforeSet[entry]; !ok {
			added = append(added, entry)
		}
	}

	for _, entry := range before {
		if _, ok := afterSet[entry]; !ok {
			removed = append(removed, entry)
		}
	}

	sort.Strings(added)
	sort.Strings(removed)

	return added, removed
}

// preview renders a value for the log, shortening it and making whitespace
// visible, so that a change in a long description stays readable.
func preview(value string) string {
	if value == "" {
		return `""`
	}

	flattened := strings.Join(strings.Fields(value), " ")
	if len(flattened) > maxValuePreview {
		flattened = flattened[:maxValuePreview] + "…"
	}

	return fmt.Sprintf("%q", flattened)
}
