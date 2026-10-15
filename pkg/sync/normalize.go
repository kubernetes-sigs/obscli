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
	"encoding/xml"
	"sort"
	"strings"

	"github.com/google/go-cmp/cmp"

	"sigs.k8s.io/release-sdk/obs"
)

// NormalizeProject returns a canonical copy of a project meta document. The
// input is never modified.
//
// Normalization removes every difference that OBS does not consider
// meaningful, so that comparing a desired document to a live one never reports
// a change that a write would not actually make:
//
//   - the kind of a standard project, which OBS omits;
//   - surrounding whitespace of the free text fields;
//   - the order of person, group and release target elements, none of which
//     carries meaning;
//   - duplicated role assignments, which OBS collapses;
//   - the difference between an absent and an empty list.
//
// The order of repositories, of the paths within a repository and of the
// architectures within a repository is meaningful to OBS and is preserved.
func NormalizeProject(project *obs.Project) *obs.Project {
	if project == nil {
		return nil
	}

	normalized := &obs.Project{
		XMLName:        xml.Name{},
		Name:           project.Name,
		Kind:           canonicalKind(project.Kind),
		Title:          trim(project.Title),
		Description:    trim(project.Description),
		URL:            trim(project.URL),
		Persons:        normalizePersons(project.Persons),
		Groups:         normalizeGroups(project.Groups),
		Build:          normalizeFlag(project.Build),
		Publish:        normalizeFlag(project.Publish),
		UseForBuild:    normalizeFlag(project.UseForBuild),
		DebugInfo:      normalizeFlag(project.DebugInfo),
		BinaryDownload: normalizeFlag(project.BinaryDownload),
	}

	if len(project.Repositories) > 0 {
		normalized.Repositories = make([]obs.Repository, 0, len(project.Repositories))
		for i := range project.Repositories {
			normalized.Repositories = append(normalized.Repositories, normalizeRepository(&project.Repositories[i]))
		}
	}

	return normalized
}

// NormalizePackage returns a canonical copy of a package meta document. The
// project attribute is dropped, because OBS reports it on a read while it is
// implied by the endpoint on a write.
func NormalizePackage(pkg *obs.Package) *obs.Package {
	normalized := normalizePackagePreservingProject(pkg)
	if normalized == nil {
		return nil
	}

	normalized.Project = ""

	return normalized
}

// normalizePackagePreservingProject normalizes a package but keeps the project
// attribute, which the document written to OBS carries.
func normalizePackagePreservingProject(pkg *obs.Package) *obs.Package {
	if pkg == nil {
		return nil
	}

	normalized := &obs.Package{
		XMLName:        xml.Name{},
		Name:           pkg.Name,
		Project:        pkg.Project,
		Title:          trim(pkg.Title),
		Description:    trim(pkg.Description),
		URL:            trim(pkg.URL),
		ReleaseName:    pkg.ReleaseName,
		BcntSyncTag:    pkg.BcntSyncTag,
		Persons:        normalizePersons(pkg.Persons),
		Groups:         normalizeGroups(pkg.Groups),
		Build:          normalizeFlag(pkg.Build),
		Publish:        normalizeFlag(pkg.Publish),
		UseForBuild:    normalizeFlag(pkg.UseForBuild),
		DebugInfo:      normalizeFlag(pkg.DebugInfo),
		BinaryDownload: normalizeFlag(pkg.BinaryDownload),
	}

	if pkg.Devel != nil {
		devel := *pkg.Devel
		normalized.Devel = &devel
	}

	return normalized
}

// ProjectsEqual reports whether two project meta documents describe the same
// state. Both documents are normalized first.
func ProjectsEqual(desired, live *obs.Project) bool {
	return cmp.Equal(NormalizeProject(desired), NormalizeProject(live))
}

// PackagesEqual reports whether two package meta documents describe the same
// state. Both documents are normalized first.
func PackagesEqual(desired, live *obs.Package) bool {
	return cmp.Equal(NormalizePackage(desired), NormalizePackage(live))
}

// normalizeRepository canonicalizes a single repository.
func normalizeRepository(repository *obs.Repository) obs.Repository {
	normalized := obs.Repository{
		Name:        repository.Name,
		Rebuild:     repository.Rebuild,
		Block:       repository.Block,
		LinkedBuild: repository.LinkedBuild,
	}

	if len(repository.Architectures) > 0 {
		normalized.Architectures = append([]string(nil), repository.Architectures...)
	}

	if len(repository.Paths) > 0 {
		normalized.Paths = append([]obs.RepositoryPath(nil), repository.Paths...)
	}

	if len(repository.ReleaseTargets) > 0 {
		targets := append([]obs.ReleaseTarget(nil), repository.ReleaseTargets...)

		sort.SliceStable(targets, func(i, j int) bool {
			if targets[i].Project != targets[j].Project {
				return targets[i].Project < targets[j].Project
			}

			return targets[i].Repository < targets[j].Repository
		})

		normalized.ReleaseTargets = targets
	}

	return normalized
}

// normalizePersons sorts role assignments and removes duplicates.
func normalizePersons(people []obs.Person) []obs.Person {
	return normalizeAssignments(people,
		func(person obs.Person) obs.Role { return person.Role },
		func(person obs.Person) string { return person.UserID },
	)
}

// normalizeGroups sorts role assignments and removes duplicates.
func normalizeGroups(groups []obs.Group) []obs.Group {
	return normalizeAssignments(groups,
		func(group obs.Group) obs.Role { return group.Role },
		func(group obs.Group) string { return group.GroupID },
	)
}

// normalizeAssignments sorts role assignments by role rank and subject, and
// removes duplicates.
func normalizeAssignments[T comparable](assignments []T, role func(T) obs.Role, subject func(T) string) []T {
	if len(assignments) == 0 {
		return nil
	}

	seen := make(map[T]struct{}, len(assignments))
	unique := make([]T, 0, len(assignments))

	for _, assignment := range assignments {
		if _, ok := seen[assignment]; ok {
			continue
		}

		seen[assignment] = struct{}{}

		unique = append(unique, assignment)
	}

	sort.Slice(unique, func(i, j int) bool {
		if roleI, roleJ := role(unique[i]), role(unique[j]); roleI != roleJ {
			if rank, otherRank := roleRank(roleI), roleRank(roleJ); rank != otherRank {
				return rank < otherRank
			}

			return roleI < roleJ
		}

		return subject(unique[i]) < subject(unique[j])
	})

	return unique
}

// normalizeFlag collapses an empty flag to an absent one. The rule order is
// meaningful to OBS and is preserved.
func normalizeFlag(flag obs.Flag) obs.Flag {
	if len(flag) == 0 {
		return nil
	}

	return append(obs.Flag(nil), flag...)
}

// roleRank orders roles by the position they have in the configuration, so
// that a generated document reads the way the configuration does. Unknown
// roles sort last, in a stable way.
func roleRank(role obs.Role) int {
	for rank, known := range roleOrder {
		if known == role {
			return rank
		}
	}

	return len(roleOrder)
}

// trim removes surrounding whitespace, most importantly the trailing newline a
// YAML block scalar adds, which OBS never stores.
func trim(value string) string {
	return strings.TrimSpace(value)
}
