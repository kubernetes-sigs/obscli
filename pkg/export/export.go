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

// Package export turns the live state of an OBS instance into obscli
// configuration files.
package export

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/release-sdk/obs"

	"sigs.k8s.io/obscli/pkg/config"
)

// Client is everything the export needs from the OBS API. It is satisfied by
// *obs.OBS.
type Client interface {
	GetProjectMeta(ctx context.Context, projectName string) (*obs.Project, error)
	ListSubprojects(ctx context.Context, prefix string) ([]string, error)
	ListPackages(ctx context.Context, projectName string) ([]string, error)
	GetPackageMeta(ctx context.Context, projectName, packageName string) (*obs.Package, error)
}

// Options configures an export.
type Options struct {
	// Project is the root project to export.
	Project string

	// Recursive includes every subproject of the root.
	Recursive bool

	// WithPackages includes the package list and the package meta.
	WithPackages bool
}

// Export reads the live state and returns it as configuration, ordered so that
// a parent always comes before its children.
func Export(ctx context.Context, client Client, opts Options) ([]*config.Project, error) {
	if !config.ValidProjectName(opts.Project) {
		return nil, fmt.Errorf("%q is not a valid OBS project name", opts.Project)
	}

	if !strings.Contains(opts.Project, ":") {
		// Exporting a bare top level name would render a namespace as a
		// project, which obscli refuses to manage and so cannot load back.
		return nil, fmt.Errorf(
			"%q is a top level name, which obscli never manages; export a project below it, such as %s:kubernetes",
			opts.Project, opts.Project,
		)
	}

	names, err := projectNames(ctx, client, opts)
	if err != nil {
		return nil, err
	}

	projects := make([]*config.Project, 0, len(names))

	for _, name := range names {
		meta, err := client.GetProjectMeta(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("exporting project %s: %w", name, err)
		}

		if meta == nil {
			return nil, fmt.Errorf("exporting project %s: OBS returned no project metadata", name)
		}

		if meta.Name != name {
			return nil, fmt.Errorf(
				"exporting project %s: OBS returned metadata for %q",
				name, meta.Name,
			)
		}

		project := ProjectConfig(meta)

		if opts.WithPackages {
			packages, err := exportPackages(ctx, client, name)
			if err != nil {
				return nil, err
			}

			project.Packages.Managed = packages
		}

		projects = append(projects, project)
	}

	return projects, nil
}

// projectNames collects the projects to export, in hierarchy order.
func projectNames(ctx context.Context, client Client, opts Options) ([]string, error) {
	names := []string{opts.Project}
	seen := map[string]struct{}{opts.Project: {}}

	if opts.Recursive {
		subprojects, err := client.ListSubprojects(ctx, opts.Project)
		if err != nil {
			return nil, fmt.Errorf("listing subprojects of %s: %w", opts.Project, err)
		}

		for _, name := range subprojects {
			if !config.ValidProjectName(name) {
				return nil, fmt.Errorf("listing subprojects of %s: OBS returned invalid project name %q", opts.Project, name)
			}

			if !strings.HasPrefix(name, opts.Project+":") {
				return nil, fmt.Errorf("listing subprojects of %s: OBS returned unrelated project %q", opts.Project, name)
			}

			if _, exists := seen[name]; exists {
				return nil, fmt.Errorf("listing subprojects of %s: OBS returned project %q more than once", opts.Project, name)
			}

			seen[name] = struct{}{}
			names = append(names, name)
		}
	}

	sort.Slice(names, func(i, j int) bool {
		if depth, otherDepth := depth(names[i]), depth(names[j]); depth != otherDepth {
			return depth < otherDepth
		}

		return names[i] < names[j]
	})

	return names, nil
}

// exportPackages reads the packages of a project, skipping the timestamped
// copies the OBS release mechanism creates.
func exportPackages(ctx context.Context, client Client, projectName string) ([]config.Package, error) {
	names, err := client.ListPackages(ctx, projectName)
	if err != nil {
		return nil, fmt.Errorf("listing packages of project %s: %w", projectName, err)
	}

	names = obs.FilterReleaseCopies(names)
	sort.Strings(names)

	packages := make([]config.Package, 0, len(names))
	seen := make(map[string]struct{}, len(names))

	for _, name := range names {
		if !config.ValidPackageName(name) {
			return nil, fmt.Errorf("listing packages of project %s: OBS returned invalid package name %q", projectName, name)
		}

		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("listing packages of project %s: OBS returned package %q more than once", projectName, name)
		}

		seen[name] = struct{}{}

		meta, err := client.GetPackageMeta(ctx, projectName, name)
		if err != nil {
			return nil, fmt.Errorf("reading meta of package %s/%s: %w", projectName, name, err)
		}

		if meta == nil {
			return nil, fmt.Errorf("reading meta of package %s/%s: OBS returned no package metadata", projectName, name)
		}

		if meta.Name != name || (meta.Project != "" && meta.Project != projectName) {
			return nil, fmt.Errorf(
				"reading meta of package %s/%s: OBS returned metadata for %s/%s",
				projectName, name, meta.Project, meta.Name,
			)
		}

		packages = append(packages, PackageConfig(meta))
	}

	return packages, nil
}

// ProjectConfig turns a live project meta document into configuration.
func ProjectConfig(meta *obs.Project) *config.Project {
	project := &config.Project{
		Name:        meta.Name,
		Kind:        projectKind(meta.Kind),
		Title:       strings.TrimSpace(meta.Title),
		Description: strings.TrimSpace(meta.Description),
		URL:         strings.TrimSpace(meta.URL),
		People:      peopleFromPersons(meta.Persons),
		Groups:      peopleFromGroups(meta.Groups),
		Flags: config.Flags{
			Build:       config.Flag(meta.Build),
			Publish:     config.Flag(meta.Publish),
			UseForBuild: config.Flag(meta.UseForBuild),
			DebugInfo:   config.Flag(meta.DebugInfo),
		},
	}

	if len(meta.Repositories) > 0 {
		project.Repositories = make([]config.Repository, 0, len(meta.Repositories))
		for i := range meta.Repositories {
			project.Repositories = append(project.Repositories, repositoryConfig(&meta.Repositories[i]))
		}
	}

	return project
}

// PackageConfig turns a live package meta document into configuration.
func PackageConfig(meta *obs.Package) config.Package {
	return config.Package{
		Name:        meta.Name,
		Title:       strings.TrimSpace(meta.Title),
		Description: strings.TrimSpace(meta.Description),
		URL:         strings.TrimSpace(meta.URL),
		People:      peopleFromPersons(meta.Persons),
		Groups:      peopleFromGroups(meta.Groups),
		Flags: config.Flags{
			Build:       config.Flag(meta.Build),
			Publish:     config.Flag(meta.Publish),
			UseForBuild: config.Flag(meta.UseForBuild),
			DebugInfo:   config.Flag(meta.DebugInfo),
		},
	}
}

// repositoryConfig turns a live repository into configuration.
func repositoryConfig(repository *obs.Repository) config.Repository {
	result := config.Repository{
		Name:          repository.Name,
		Architectures: repository.Architectures,
	}

	if len(repository.Paths) > 0 {
		result.Paths = make([]config.RepositoryPath, 0, len(repository.Paths))
		for _, path := range repository.Paths {
			result.Paths = append(result.Paths, config.RepositoryPath{
				Project:    path.Project,
				Repository: path.Repository,
			})
		}
	}

	if len(repository.ReleaseTargets) > 0 {
		result.ReleaseTargets = make([]config.ReleaseTarget, 0, len(repository.ReleaseTargets))
		for _, target := range repository.ReleaseTargets {
			result.ReleaseTargets = append(result.ReleaseTargets, config.ReleaseTarget{
				Project:    target.Project,
				Repository: target.Repository,
				Trigger:    target.Trigger,
			})
		}
	}

	return result
}

// peopleFromPersons groups user role assignments by role.
func peopleFromPersons(persons []obs.Person) config.People {
	byRole := make(map[obs.Role][]string, len(persons))
	for _, person := range persons {
		byRole[person.Role] = append(byRole[person.Role], person.UserID)
	}

	return peopleFromRoles(byRole)
}

// peopleFromGroups groups group role assignments by role.
func peopleFromGroups(groups []obs.Group) config.People {
	byRole := make(map[obs.Role][]string, len(groups))
	for _, group := range groups {
		byRole[group.Role] = append(byRole[group.Role], group.GroupID)
	}

	return peopleFromRoles(byRole)
}

// peopleFromRoles fills the role keyed configuration fields, sorting every
// list so that the generated file is reproducible.
func peopleFromRoles(byRole map[obs.Role][]string) config.People {
	for _, subjects := range byRole {
		sort.Strings(subjects)
	}

	return config.People{
		Maintainers: byRole[obs.RoleMaintainer],
		Bugowners:   byRole[obs.RoleBugOwner],
		Reviewers:   byRole[obs.RoleReviewer],
		Readers:     byRole[obs.RoleReader],
		Downloaders: byRole[obs.RoleDownloader],
	}
}

// projectKind drops the kind of a standard project, which OBS omits anyway.
func projectKind(kind obs.ProjectKind) obs.ProjectKind {
	if kind == obs.ProjectKindStandard {
		return ""
	}

	return kind
}

// depth is the number of colon separated segments of a project name.
func depth(name string) int {
	return strings.Count(name, ":")
}
