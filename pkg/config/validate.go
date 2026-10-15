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

package config

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/release-sdk/obs"
)

// ValidateOptions tunes the offline checks run by Validate.
type ValidateOptions struct {
	// RequiredMaintainers must be effective maintainers of every declared
	// project. CI uses it to guarantee that the release bot can never be
	// locked out of the project tree.
	RequiredMaintainers []string
}

// Validate runs every offline check against the desired state. It never talks
// to OBS and never needs credentials. All problems are reported at once, so
// that a single run surfaces every mistake.
func Validate(config *Config, opts ValidateOptions) error {
	if config == nil || len(config.Projects) == 0 {
		return errors.New("configuration declares no projects")
	}

	var errs []error

	byName := make(map[string]*Project, len(config.Projects))

	for _, project := range config.Projects {
		if project.Name == "" {
			continue
		}

		if previous, ok := byName[project.Name]; ok {
			errs = append(errs, fmt.Errorf(
				"%s: project %q is already declared in %s",
				project.SourcePath(), project.Name, previous.SourcePath(),
			))

			continue
		}

		byName[project.Name] = project
	}

	for _, project := range config.Projects {
		errs = append(errs, validateProject(project, byName)...)
	}

	errs = append(errs, validateHierarchy(config, byName)...)
	errs = append(errs, validateOwnership(config, byName, opts)...)

	return errors.Join(errs...)
}

// validateHierarchy rejects a gap in the declared project tree: an undeclared
// project that sits between two declared ones.
//
// The check is confined to the declared set, which is the only tree obscli
// knows about. Walking up from a project, the first declared ancestor anchors
// it, and anything undeclared passed on the way there is a gap, because obscli
// would have to create a project it neither owns nor describes in order to
// reach the one below it. A project that finds no declared ancestor at all is
// a root of the declared tree, and nothing above a root is obscli's business.
//
// This makes the check independent of what OBS happens to contain, so a scoped
// run reconciling a single deep project is a single node tree with nothing to
// check, while the same project in a full run must have its whole chain of
// parents declared.
func validateHierarchy(config *Config, byName map[string]*Project) []error {
	var errs []error

	for _, project := range config.Projects {
		if byName[project.Name] != project {
			continue
		}

		var gaps []string

		for _, ancestor := range Ancestors(project.Name) {
			if _, ok := byName[ancestor]; !ok {
				gaps = append(gaps, ancestor)

				continue
			}

			for _, gap := range gaps {
				errs = append(errs, fmt.Errorf(
					"%s: project %q sits below the declared project %q, but the project %q between them is not declared",
					project.SourcePath(), project.Name, ancestor, gap,
				))
			}

			break
		}
	}

	return errs
}

// validateProject checks a project and its references to declared repositories.
func validateProject(project *Project, byName map[string]*Project) []error {
	var errs []error

	source := project.SourcePath()
	if source == "" {
		source = "<config>"
	}

	switch {
	case project.Name == "":
		errs = append(errs, fmt.Errorf("%s: name is required", source))
	case !ValidProjectName(project.Name):
		errs = append(errs, fmt.Errorf("%s: %q is not a valid OBS project name", source, project.Name))
	case !strings.Contains(project.Name, ":"):
		// A bare top level name such as isv or home is a namespace that whole
		// trees live under, not a project obscli may own. Managing one would
		// put every unrelated tree below it within reach of a single apply.
		errs = append(errs, fmt.Errorf(
			"%s: %q is a top level name, which obscli never manages; the shallowest manageable project has a parent, such as %s:kubernetes",
			source, project.Name, project.Name,
		))
	}

	if err := validateProjectKind(project.Kind); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", source, err))
	}

	errs = append(errs, validatePeople(source, "", &project.People, &project.Groups)...)
	errs = append(errs, validateRepositories(source, project.Name, project.Repositories, byName)...)
	errs = append(errs, validatePackages(source, project.Packages)...)
	errs = append(errs, validateSubprojects(source, project.Subprojects)...)

	return errs
}

// validateProjectKind rejects a kind OBS does not know.
func validateProjectKind(kind obs.ProjectKind) error {
	switch kind {
	case "",
		obs.ProjectKindStandard,
		obs.ProjectKindMaintenance,
		obs.ProjectKindMaintenanceIncident,
		obs.ProjectKindMaintenanceRelease:
		return nil
	default:
		return fmt.Errorf("invalid kind %q, must be one of: %s, %s, %s, %s",
			kind,
			obs.ProjectKindStandard,
			obs.ProjectKindMaintenance,
			obs.ProjectKindMaintenanceIncident,
			obs.ProjectKindMaintenanceRelease,
		)
	}
}

// validatePeople checks user and group role assignments. The scope describes
// where the assignment lives and is empty for the project itself.
func validatePeople(source, scope string, users, groups *People) []error {
	userAssignments := sortedByRole(users)
	groupAssignments := sortedByRole(groups)

	errs := make([]error, 0, len(userAssignments)+len(groupAssignments))

	for _, assignment := range userAssignments {
		errs = append(errs, validateSubjects(
			source, scope, assignment.Role, "user", assignment.Subjects, ValidUserName,
		)...)
	}

	for _, assignment := range groupAssignments {
		errs = append(errs, validateSubjects(
			source, scope, assignment.Role, "group", assignment.Subjects, ValidGroupName,
		)...)
	}

	return errs
}

// validateSubjects checks the names of a single role assignment and rejects
// duplicates, which would produce a redundant element in the OBS meta.
func validateSubjects(
	source, scope string,
	role obs.Role,
	kind string,
	subjects []string,
	valid func(string) bool,
) []error {
	var errs []error

	seen := make(map[string]struct{}, len(subjects))

	for _, subject := range subjects {
		if !valid(subject) {
			errs = append(errs, fmt.Errorf(
				"%s: %s%s %q is not a valid OBS %s name",
				source, scope, role, subject, kind,
			))
		}

		if _, ok := seen[subject]; ok {
			errs = append(errs, fmt.Errorf(
				"%s: %s%s %q is listed more than once", source, scope, role, subject,
			))
		}

		seen[subject] = struct{}{}
	}

	return errs
}

// validateRepositories checks the build repositories of a project.
func validateRepositories(source, projectName string, repositories []Repository, byName map[string]*Project) []error {
	var errs []error

	seen := make(map[string]struct{}, len(repositories))

	for _, repository := range repositories {
		if repository.Name == "" {
			errs = append(errs, fmt.Errorf("%s: repository name is required", source))

			continue
		}

		if _, ok := seen[repository.Name]; ok {
			errs = append(errs, fmt.Errorf(
				"%s: repository %q is declared more than once", source, repository.Name,
			))
		}

		seen[repository.Name] = struct{}{}

		scope := fmt.Sprintf("repository %s: ", repository.Name)
		from := RepositoryPath{Project: projectName, Repository: repository.Name}

		errs = append(errs, validateArchitectures(source, scope, repository.Architectures)...)

		for _, path := range repository.Paths {
			errs = append(errs, validateReference(source, scope+"path", from, path, byName)...)
		}

		for _, target := range repository.ReleaseTargets {
			errs = append(errs, validateReference(
				source, scope+"release target", from,
				RepositoryPath{Project: target.Project, Repository: target.Repository}, byName,
			)...)

			if err := validateReleaseTrigger(target.Trigger); err != nil {
				errs = append(errs, fmt.Errorf("%s: %s%w", source, scope, err))
			}
		}
	}

	return errs
}

// validateArchitectures rejects empty and duplicated architectures.
func validateArchitectures(source, scope string, architectures []string) []error {
	var errs []error

	seen := make(map[string]struct{}, len(architectures))

	for _, arch := range architectures {
		if arch == "" {
			errs = append(errs, fmt.Errorf("%s: %sarchitecture must not be empty", source, scope))

			continue
		}

		if _, ok := seen[arch]; ok {
			errs = append(errs, fmt.Errorf(
				"%s: %sarchitecture %q is listed more than once", source, scope, arch,
			))
		}

		seen[arch] = struct{}{}
	}

	return errs
}

// validateReference checks the shape of a repository reference and resolves it
// when its target project is declared. External projects are not looked up.
func validateReference(source, scope string, from, to RepositoryPath, byName map[string]*Project) []error {
	var errs []error

	switch {
	case to.Project == "":
		errs = append(errs, fmt.Errorf("%s: %s requires a project", source, scope))
	case !ValidProjectName(to.Project):
		errs = append(errs, fmt.Errorf(
			"%s: %s refers to %q, which is not a valid OBS project name", source, scope, to.Project,
		))
	}

	if to.Repository == "" {
		errs = append(errs, fmt.Errorf("%s: %s requires a repository", source, scope))
	}

	if len(errs) > 0 {
		return errs
	}

	if from == to {
		return []error{fmt.Errorf(
			"%s: %s must not refer to its own repository %s/%s",
			source, scope, from.Project, from.Repository,
		)}
	}

	if target, declared := byName[to.Project]; declared {
		if !slices.ContainsFunc(target.Repositories, func(repository Repository) bool {
			return repository.Name == to.Repository
		}) {
			errs = append(errs, fmt.Errorf(
				"%s: %s refers to repository %q, which is not declared in project %q",
				source, scope, to.Repository, to.Project,
			))
		}
	}

	return errs
}

// validateReleaseTrigger rejects a trigger OBS does not know.
func validateReleaseTrigger(trigger obs.ReleaseTrigger) error {
	switch trigger {
	case "",
		obs.ReleaseTriggerManual,
		obs.ReleaseTriggerMaintenance,
		obs.ReleaseTriggerObsGenDiff:
		return nil
	default:
		return fmt.Errorf("invalid release target trigger %q, must be one of: %s, %s, %s",
			trigger,
			obs.ReleaseTriggerManual,
			obs.ReleaseTriggerMaintenance,
			obs.ReleaseTriggerObsGenDiff,
		)
	}
}

// validateSubprojects checks the subproject policy.
func validateSubprojects(source string, subprojects Subprojects) []error {
	if subprojects.Policy != "" && !slices.Contains(Policies, subprojects.Policy) {
		return []error{fmt.Errorf(
			"%s: invalid subproject policy %q, must be one of: %s, %s, %s",
			source, subprojects.Policy,
			PolicyIgnore, PolicyReport, PolicyPrune,
		)}
	}

	return nil
}

// validatePackages checks the package policy and the managed package list.
func validatePackages(source string, packages Packages) []error {
	var errs []error

	if packages.Policy != "" && !slices.Contains(Policies, packages.Policy) {
		errs = append(errs, fmt.Errorf(
			"%s: invalid package policy %q, must be one of: %s, %s, %s",
			source, packages.Policy,
			PolicyIgnore, PolicyReport, PolicyPrune,
		))
	}

	seen := make(map[string]struct{}, len(packages.Managed))

	for i := range packages.Managed {
		pkg := &packages.Managed[i]

		switch {
		case pkg.Name == "":
			errs = append(errs, fmt.Errorf("%s: package name is required", source))

			continue
		case !ValidPackageName(pkg.Name):
			errs = append(errs, fmt.Errorf(
				"%s: %q is not a valid OBS package name", source, pkg.Name,
			))
		case obs.IsReleaseCopy(pkg.Name):
			errs = append(errs, fmt.Errorf(
				"%s: package %q is a release copy and must not be managed", source, pkg.Name,
			))
		}

		if _, ok := seen[pkg.Name]; ok {
			errs = append(errs, fmt.Errorf(
				"%s: package %q is declared more than once", source, pkg.Name,
			))
		}

		seen[pkg.Name] = struct{}{}

		scope := fmt.Sprintf("package %s: ", pkg.Name)
		errs = append(errs, validatePeople(source, scope, &pkg.People, &pkg.Groups)...)
	}

	return errs
}

// validateOwnership runs the safety checks: every project needs at least one
// effective maintainer, and every required maintainer must be effective on
// every project.
func validateOwnership(config *Config, byName map[string]*Project, opts ValidateOptions) []error {
	var errs []error

	for _, project := range config.Projects {
		if project.Name == "" {
			continue
		}

		maintainers := effectiveMaintainers(project, byName)

		if len(maintainers) == 0 {
			errs = append(errs, fmt.Errorf(
				"%s: project %q has no maintainer, neither declared nor inherited from a declared ancestor",
				project.SourcePath(), project.Name,
			))
		}

		for _, required := range opts.RequiredMaintainers {
			if _, ok := maintainers[required]; !ok {
				errs = append(errs, fmt.Errorf(
					"%s: project %q must have %q as a maintainer",
					project.SourcePath(), project.Name, required,
				))
			}
		}
	}

	return errs
}

// effectiveMaintainers collects the maintainers of a project, including those
// inherited from ancestors that are part of the configuration. OBS itself
// inherits permissions down the project tree, so an ancestor maintainer is a
// maintainer of the project as well.
func effectiveMaintainers(project *Project, byName map[string]*Project) map[string]struct{} {
	maintainers := make(map[string]struct{})

	for _, maintainer := range project.People.Maintainers {
		maintainers[maintainer] = struct{}{}
	}

	// Walk the whole ancestor chain instead of stopping at the first ancestor
	// that is missing, because a scoped run may declare only part of the tree.
	name, hasParent := project.Parent()
	for hasParent {
		if ancestor, declared := byName[name]; declared {
			for _, maintainer := range ancestor.People.Maintainers {
				maintainers[maintainer] = struct{}{}
			}
		}

		index := strings.LastIndex(name, ":")
		if index < 0 {
			break
		}

		name = name[:index]
	}

	return maintainers
}

// roleAssignment is a single role and the subjects it is assigned to.
type roleAssignment struct {
	Role     obs.Role
	Subjects []string
}

// sortedByRole returns the role assignments in a stable order, so that the
// reported problems do not depend on map iteration order.
func sortedByRole(people *People) []roleAssignment {
	byRole := people.ByRole()

	assignments := make([]roleAssignment, 0, len(byRole))
	for role, subjects := range byRole {
		assignments = append(assignments, roleAssignment{Role: role, Subjects: subjects})
	}

	sort.Slice(assignments, func(i, j int) bool { return assignments[i].Role < assignments[j].Role })

	return assignments
}
