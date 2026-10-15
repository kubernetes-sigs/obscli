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
	"sigs.k8s.io/release-sdk/obs"

	"sigs.k8s.io/obscli/pkg/config"
)

// roleOrder is the order role assignments are emitted in, so that a generated
// meta document is byte for byte reproducible.
var roleOrder = []obs.Role{
	obs.RoleMaintainer,
	obs.RoleBugOwner,
	obs.RoleReviewer,
	obs.RoleReader,
	obs.RoleDownloader,
}

// ProjectMeta turns the desired state of a project into the OBS meta document
// that is written to the API. The result is already canonical, so that it can
// be compared to a normalized live document without producing false diffs.
func ProjectMeta(project *config.Project) *obs.Project {
	meta := &obs.Project{
		Name:        project.Name,
		Kind:        canonicalKind(project.Kind),
		Title:       trim(project.Title),
		Description: trim(project.Description),
		URL:         trim(project.URL),
		Persons:     persons(&project.People),
		Groups:      groups(&project.Groups),
		Build:       obs.Flag(project.Flags.Build),
		Publish:     obs.Flag(project.Flags.Publish),
		UseForBuild: obs.Flag(project.Flags.UseForBuild),
		DebugInfo:   obs.Flag(project.Flags.DebugInfo),
	}

	if len(project.Repositories) > 0 {
		meta.Repositories = make([]obs.Repository, 0, len(project.Repositories))
		for i := range project.Repositories {
			meta.Repositories = append(meta.Repositories, repositoryMeta(&project.Repositories[i]))
		}
	}

	return NormalizeProject(meta)
}

// MinimalProjectMeta is the meta a project is created with, before the full
// desired state is applied. OBS requires title and description to be present,
// so an otherwise empty project header is the smallest valid document.
func MinimalProjectMeta(name string) *obs.Project {
	return &obs.Project{Name: name}
}

// PackageMeta turns the desired state of a package into the OBS meta document
// that is written to the API.
func PackageMeta(projectName string, pkg *config.Package) *obs.Package {
	meta := &obs.Package{
		Name:        pkg.Name,
		Project:     projectName,
		Title:       trim(pkg.Title),
		Description: trim(pkg.Description),
		URL:         trim(pkg.URL),
		Persons:     persons(&pkg.People),
		Groups:      groups(&pkg.Groups),
		Build:       obs.Flag(pkg.Flags.Build),
		Publish:     obs.Flag(pkg.Flags.Publish),
		UseForBuild: obs.Flag(pkg.Flags.UseForBuild),
		DebugInfo:   obs.Flag(pkg.Flags.DebugInfo),
	}

	return normalizePackagePreservingProject(meta)
}

// repositoryMeta converts a single repository. The order of paths and
// architectures is significant to OBS and is preserved as declared.
func repositoryMeta(repository *config.Repository) obs.Repository {
	meta := obs.Repository{Name: repository.Name}

	if len(repository.Architectures) > 0 {
		meta.Architectures = append([]string(nil), repository.Architectures...)
	}

	if len(repository.Paths) > 0 {
		meta.Paths = make([]obs.RepositoryPath, 0, len(repository.Paths))
		for _, path := range repository.Paths {
			meta.Paths = append(meta.Paths, obs.RepositoryPath{
				Project:    path.Project,
				Repository: path.Repository,
			})
		}
	}

	if len(repository.ReleaseTargets) > 0 {
		meta.ReleaseTargets = make([]obs.ReleaseTarget, 0, len(repository.ReleaseTargets))
		for _, target := range repository.ReleaseTargets {
			meta.ReleaseTargets = append(meta.ReleaseTargets, obs.ReleaseTarget{
				Project:    target.Project,
				Repository: target.Repository,
				Trigger:    target.Trigger,
			})
		}
	}

	return meta
}

// persons turns role keyed user lists into OBS person elements.
func persons(people *config.People) []obs.Person {
	byRole := people.ByRole()

	var result []obs.Person

	for _, role := range roleOrder {
		for _, userID := range byRole[role] {
			result = append(result, obs.Person{UserID: userID, Role: role})
		}
	}

	return result
}

// groups turns role keyed group lists into OBS group elements.
func groups(people *config.People) []obs.Group {
	byRole := people.ByRole()

	var result []obs.Group

	for _, role := range roleOrder {
		for _, groupID := range byRole[role] {
			result = append(result, obs.Group{GroupID: groupID, Role: role})
		}
	}

	return result
}

// canonicalKind drops the kind of a standard project, which OBS represents by
// an absent attribute rather than by an explicit value.
func canonicalKind(kind obs.ProjectKind) obs.ProjectKind {
	if kind == obs.ProjectKindStandard {
		return ""
	}

	return kind
}
