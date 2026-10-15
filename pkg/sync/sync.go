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

// Package sync reconciles the desired state described by the configuration
// with the live state of an OBS instance.
package sync

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/release-sdk/obs"

	"sigs.k8s.io/obscli/pkg/config"
)

// projectReader reads project state.
type projectReader interface {
	GetProjectMeta(ctx context.Context, projectName string) (*obs.Project, error)
	ListPackages(ctx context.Context, projectName string) ([]string, error)
	ListSubprojects(ctx context.Context, prefix string) ([]string, error)
}

// projectWriter writes project state.
type projectWriter interface {
	PutProjectMeta(ctx context.Context, project *obs.Project) error
	DeleteProject(ctx context.Context, projectName string) error
}

// packageReader reads package state.
type packageReader interface {
	GetPackageMeta(ctx context.Context, projectName, packageName string) (*obs.Package, error)
}

// packageWriter writes package state.
type packageWriter interface {
	PutPackageMeta(ctx context.Context, pkg *obs.Package) error
	DeletePackage(ctx context.Context, projectName, packageName string) error
}

// Client is everything the reconciler needs from the OBS API. It is satisfied
// by *obs.OBS.
type Client interface {
	projectReader
	projectWriter
	packageReader
	packageWriter
}

// Options configures a reconciliation.
type Options struct {
	// Confirm turns the dry run into a real run. Without it the reconciler
	// produces the same plan and summary but never calls a mutating client
	// method.
	Confirm bool

	// AllowDeletions permits deletions. Without it a deletion candidate is
	// only reported, which makes an unattended run unable to destroy anything.
	AllowDeletions bool
}

// deletionsAllowed reports whether deletion candidates are part of the plan.
// Confirm independently controls whether any planned mutation is sent.
func (o Options) deletionsAllowed() bool {
	return o.AllowDeletions
}

// Summary counts what a reconciliation did, or would do in a dry run.
type Summary struct {
	ProjectsCreated int
	ProjectsUpdated int
	ProjectsDeleted int
	PackagesCreated int
	PackagesUpdated int
	PackagesDeleted int

	// Reported counts the resources that are undeclared but left alone,
	// either because the policy only reports them or because deletions are
	// not enabled.
	Reported int
}

// Changed reports whether the reconciliation found anything to do.
func (s Summary) Changed() bool {
	return s.ProjectsCreated+s.ProjectsUpdated+s.ProjectsDeleted+
		s.PackagesCreated+s.PackagesUpdated+s.PackagesDeleted > 0
}

type packageDeletion struct {
	projectName string
	packageName string
}

// reconciler carries the state of a single run.
type reconciler struct {
	client  Client
	logger  *Logger
	options Options

	// declared maps a project name to its desired state.
	declared map[string]*config.Project

	// live tracks the latest successful metadata write, including staged
	// writes simulated during a dry run. Nil means the project does not exist.
	live map[string]*obs.Project

	// created tracks the projects this run creates, which do not exist yet
	// during a dry run.
	created map[string]struct{}

	// updated counts an existing project only once, even if staging requires
	// multiple writes. Its intended final diff is logged before the first write.
	updated map[string]struct{}

	// Package deletions wait until all creates and updates have succeeded.
	pendingPackageDeletions []packageDeletion

	summary Summary
}

// Reconcile brings the OBS instance in line with the configuration.
//
// Missing projects are created parents first, then missing repositories are
// prepared without dependencies. Desired metadata is applied in dependency
// order while retaining obsolete repositories until all updates succeed.
// Cyclic dependencies are rejected before any OBS requests are made.
// Cleanup removes obsolete repositories, then packages, then projects.
func Reconcile(
	ctx context.Context,
	client Client,
	cfg *config.Config,
	logger *Logger,
	options Options,
) (Summary, error) {
	r := &reconciler{
		client:   client,
		logger:   logger,
		options:  options,
		declared: make(map[string]*config.Project, len(cfg.Projects)),
		live:     make(map[string]*obs.Project, len(cfg.Projects)),
		created:  make(map[string]struct{}),
		updated:  make(map[string]struct{}),
	}

	projects := append([]*config.Project(nil), cfg.Projects...)
	sortByHierarchy(projects)

	for _, project := range projects {
		r.declared[project.Name] = project
	}

	metadataOrder, err := sortByDependencies(projects)
	if err != nil {
		return r.summary, err
	}

	if err := r.loadLiveProjects(ctx, projects); err != nil {
		return r.summary, err
	}

	if err := r.createMissingProjects(ctx, projects); err != nil {
		return r.summary, err
	}

	if err := r.prepareRepositories(ctx, projects); err != nil {
		return r.summary, err
	}

	if err := r.applyProjects(ctx, metadataOrder); err != nil {
		return r.summary, err
	}

	if err := r.pruneRepositories(ctx, projects); err != nil {
		return r.summary, err
	}

	if err := r.prunePackages(ctx); err != nil {
		return r.summary, err
	}

	if err := r.pruneProjects(ctx, projects); err != nil {
		return r.summary, err
	}

	return r.summary, nil
}

// loadLiveProjects fetches the current meta of every declared project.
func (r *reconciler) loadLiveProjects(ctx context.Context, projects []*config.Project) error {
	for _, project := range projects {
		meta, err := r.client.GetProjectMeta(ctx, project.Name)
		if err != nil {
			if obs.IsNotFound(err) {
				r.live[project.Name] = nil

				continue
			}

			return fmt.Errorf("loading the live state: %w", err)
		}

		if meta == nil {
			return fmt.Errorf("loading the live state of project %s: OBS returned no project metadata", project.Name)
		}

		if meta.Name != project.Name {
			return fmt.Errorf(
				"loading the live state of project %s: OBS returned metadata for %q",
				project.Name, meta.Name,
			)
		}

		r.live[project.Name] = meta
	}

	return nil
}

// createMissingProjects creates every project that does not exist yet with the
// minimal meta, parents before children. The intended full-meta change is
// logged before the minimal write, so an output failure cannot allow an
// unreported project creation.
func (r *reconciler) createMissingProjects(ctx context.Context, projects []*config.Project) error {
	for _, project := range projects {
		if r.live[project.Name] != nil {
			continue
		}

		changes := ProjectChanges(ProjectMeta(project), MinimalProjectMeta(project.Name))

		if err := r.logger.Change(0, GlyphCreate, "project", project.Name); err != nil {
			return err
		}

		if err := r.logger.Details(0, changes); err != nil {
			return err
		}

		if err := r.putProjectMeta(ctx, MinimalProjectMeta(project.Name)); err != nil {
			return fmt.Errorf("creating project %s: %w", project.Name, err)
		}

		r.summary.ProjectsCreated++
		r.created[project.Name] = struct{}{}
		r.live[project.Name] = MinimalProjectMeta(project.Name)
	}

	return nil
}

// prepareRepositories adds missing repositories without changing existing
// metadata or references. Every target must exist before any new references
// are applied, including forward release targets within the same project.
func (r *reconciler) prepareRepositories(ctx context.Context, projects []*config.Project) error {
	for _, project := range projects {
		prepared := NormalizeProject(r.live[project.Name])
		desired := ProjectMeta(project)

		for i := range desired.Repositories {
			repository := &desired.Repositories[i]
			if hasRepository(prepared.Repositories, repository.Name) {
				continue
			}

			repository.Paths = nil
			repository.ReleaseTargets = nil
			prepared.Repositories = append(prepared.Repositories, *repository)
		}

		if err := r.updateProjectMeta(ctx, project, prepared); err != nil {
			return fmt.Errorf("preparing repositories: %w", err)
		}
	}

	return nil
}

// applyProjects applies desired metadata and reconciles packages, retaining
// obsolete repositories until all consumers have been updated.
func (r *reconciler) applyProjects(ctx context.Context, projects []*config.Project) error {
	for _, project := range projects {
		if err := r.applyProject(ctx, project); err != nil {
			return err
		}
	}

	return nil
}

// applyProject reconciles the meta and the packages of a single project.
func (r *reconciler) applyProject(ctx context.Context, project *config.Project) error {
	desired := ProjectMeta(project)

	live := r.live[project.Name]
	for i := range live.Repositories {
		if hasRepository(desired.Repositories, live.Repositories[i].Name) {
			continue
		}

		desired.Repositories = append(desired.Repositories, live.Repositories[i])
	}

	if err := r.updateProjectMeta(ctx, project, desired); err != nil {
		return err
	}

	return r.applyPackages(ctx, project)
}

func hasRepository(repositories []obs.Repository, name string) bool {
	return slices.ContainsFunc(repositories, func(repository obs.Repository) bool {
		return repository.Name == name
	})
}

// pruneRepositories finishes the metadata replacement after all declared
// consumers have been updated. OBS still protects references from outside scope.
func (r *reconciler) pruneRepositories(ctx context.Context, projects []*config.Project) error {
	// Obsolete repositories can reference each other, even cyclically.
	// Detach those links only after all creates and updates have succeeded.
	for _, project := range projects {
		desired := ProjectMeta(project)

		cleaned := NormalizeProject(r.live[project.Name])
		for i := range cleaned.Repositories {
			repository := &cleaned.Repositories[i]
			if hasRepository(desired.Repositories, repository.Name) {
				continue
			}

			repository.Paths = slices.DeleteFunc(repository.Paths, func(path obs.RepositoryPath) bool {
				return r.repositoryWillBeRemoved(path.Project, path.Repository)
			})
			repository.ReleaseTargets = slices.DeleteFunc(repository.ReleaseTargets, func(target obs.ReleaseTarget) bool {
				return r.repositoryWillBeRemoved(target.Project, target.Repository)
			})
		}

		if err := r.updateProjectMeta(ctx, project, cleaned); err != nil {
			return fmt.Errorf("detaching obsolete repository references: %w", err)
		}
	}

	for _, project := range projects {
		if err := r.updateProjectMeta(ctx, project, ProjectMeta(project)); err != nil {
			return fmt.Errorf("removing obsolete repositories: %w", err)
		}
	}

	return nil
}

func (r *reconciler) repositoryWillBeRemoved(projectName, repositoryName string) bool {
	project, declared := r.declared[projectName]
	if !declared {
		return false
	}

	return !slices.ContainsFunc(project.Repositories, func(repository config.Repository) bool {
		return repository.Name == repositoryName
	})
}

// updateProjectMeta tracks intermediate state in both modes, but logs the final
// intent and counts an existing project only on its first successful mutation.
func (r *reconciler) updateProjectMeta(ctx context.Context, project *config.Project, meta *obs.Project) error {
	live := r.live[project.Name]
	if ProjectsEqual(meta, live) {
		return nil
	}

	_, isNew := r.created[project.Name]

	_, updated := r.updated[project.Name]
	if !isNew && !updated {
		if err := r.logger.Change(0, GlyphUpdate, "project", project.Name); err != nil {
			return err
		}

		if err := r.logger.Details(0, ProjectChanges(ProjectMeta(project), live)); err != nil {
			return err
		}
	}

	if err := r.putProjectMeta(ctx, meta); err != nil {
		return fmt.Errorf("writing meta of project %s: %w", project.Name, err)
	}

	r.live[project.Name] = meta
	if !isNew && !updated {
		r.updated[project.Name] = struct{}{}
		r.summary.ProjectsUpdated++
	}

	return nil
}

// applyPackages creates and updates packages, reports unmanaged packages, and
// queues permitted deletions for the final phase.
func (r *reconciler) applyPackages(ctx context.Context, project *config.Project) error {
	policy := project.Packages.PolicyOrDefault()

	existing, err := r.livePackages(ctx, project.Name)
	if err != nil {
		return err
	}

	declared := make(map[string]struct{}, len(project.Packages.Managed))

	for i := range project.Packages.Managed {
		pkg := &project.Packages.Managed[i]
		declared[pkg.Name] = struct{}{}

		if err := r.applyPackage(ctx, project.Name, pkg, existing); err != nil {
			return err
		}
	}

	if policy == config.PolicyIgnore {
		return nil
	}

	unmanaged := make([]string, 0, len(existing))

	for name := range existing {
		if _, ok := declared[name]; !ok {
			unmanaged = append(unmanaged, name)
		}
	}

	sort.Strings(unmanaged)

	for _, name := range unmanaged {
		if err := r.planPackagePruning(project.Name, name, policy); err != nil {
			return err
		}
	}

	return nil
}

// livePackages lists the packages of a project, excluding the timestamped
// copies the OBS release mechanism creates, which are never managed. A project
// this run creates has no packages yet, and during a dry run it does not even
// exist, so listing is skipped.
func (r *reconciler) livePackages(ctx context.Context, projectName string) (map[string]struct{}, error) {
	if _, isNew := r.created[projectName]; isNew {
		return map[string]struct{}{}, nil
	}

	names, err := r.client.ListPackages(ctx, projectName)
	if err != nil {
		return nil, fmt.Errorf("listing packages of project %s: %w", projectName, err)
	}

	names = obs.FilterReleaseCopies(names)

	packages := make(map[string]struct{}, len(names))
	for _, name := range names {
		packages[name] = struct{}{}
	}

	return packages, nil
}

// applyPackage creates or updates a single managed package.
func (r *reconciler) applyPackage(
	ctx context.Context,
	projectName string,
	pkg *config.Package,
	existing map[string]struct{},
) error {
	desired := PackageMeta(projectName, pkg)
	qualifiedName := projectName + "/" + pkg.Name

	if _, ok := existing[pkg.Name]; !ok {
		if err := r.logger.Change(1, GlyphCreate, "package", qualifiedName); err != nil {
			return err
		}

		if err := r.putPackageMeta(ctx, desired); err != nil {
			return fmt.Errorf("creating package %s/%s: %w", projectName, pkg.Name, err)
		}

		r.summary.PackagesCreated++

		return nil
	}

	live, err := r.client.GetPackageMeta(ctx, projectName, pkg.Name)
	if err != nil {
		return fmt.Errorf("reading meta of package %s/%s: %w", projectName, pkg.Name, err)
	}

	changes := PackageChanges(desired, live)
	if len(changes) == 0 {
		return nil
	}

	if err := r.logger.Change(1, GlyphUpdate, "package", qualifiedName); err != nil {
		return err
	}

	if err := r.logger.Details(1, changes); err != nil {
		return err
	}

	if err := r.putPackageMeta(ctx, desired); err != nil {
		return fmt.Errorf("writing meta of package %s/%s: %w", projectName, pkg.Name, err)
	}

	r.summary.PackagesUpdated++

	return nil
}

// planPackagePruning reports an unmanaged package or queues its deletion.
func (r *reconciler) planPackagePruning(
	projectName, packageName string,
	policy config.Policy,
) error {
	qualifiedName := projectName + "/" + packageName

	if policy != config.PolicyPrune {
		if err := r.logger.Change(1, GlyphReport, "package", qualifiedName); err != nil {
			return err
		}

		if err := r.logger.Detailf(1, "not declared in the configuration, policy is %s", policy); err != nil {
			return err
		}

		r.summary.Reported++

		return nil
	}

	if !r.options.deletionsAllowed() {
		if err := r.logger.Change(1, GlyphReport, "package", qualifiedName); err != nil {
			return err
		}

		if err := r.logger.Detailf(
			1, "not declared in the configuration, run with --allow-deletions to delete it",
		); err != nil {
			return err
		}

		r.summary.Reported++

		return nil
	}

	r.pendingPackageDeletions = append(r.pendingPackageDeletions, packageDeletion{
		projectName: projectName,
		packageName: packageName,
	})

	return nil
}

// prunePackages executes the deletions queued during package reconciliation.
func (r *reconciler) prunePackages(ctx context.Context) error {
	for _, pkg := range r.pendingPackageDeletions {
		qualifiedName := pkg.projectName + "/" + pkg.packageName
		if err := r.logger.Change(1, GlyphDelete, "package", qualifiedName); err != nil {
			return err
		}

		if err := r.deletePackage(ctx, pkg.projectName, pkg.packageName); err != nil {
			return fmt.Errorf("deleting package %s: %w", qualifiedName, err)
		}

		r.summary.PackagesDeleted++
	}

	return nil
}

// pruneProjects reports or deletes the projects that live below a declared
// project but are not declared themselves.
//
// An undeclared project has no configuration file of its own, so the policy
// that governs it is the `subprojects.policy` of its nearest declared
// ancestor. That makes the behaviour the exact project level counterpart of
// `packages.policy`, and it keeps a run scoped to a part of the tree unable to
// touch anything outside of that part: obscli only ever looks below projects
// the configuration actually declares.
func (r *reconciler) pruneProjects(ctx context.Context, projects []*config.Project) error {
	undeclared, err := r.undeclaredProjects(ctx, projects)
	if err != nil {
		return err
	}

	// Delete children before their parents, which OBS requires.
	sort.Sort(sort.Reverse(sort.StringSlice(undeclared)))

	for _, name := range undeclared {
		policy := r.subprojectPolicy(name)

		switch {
		case policy == config.PolicyIgnore:
			continue
		case policy != config.PolicyPrune:
			if err := r.logger.Change(0, GlyphReport, "project", name); err != nil {
				return err
			}

			if err := r.logger.Detailf(
				0, "not declared in the configuration, policy is %s", policy,
			); err != nil {
				return err
			}

			r.summary.Reported++
		case !r.options.deletionsAllowed():
			if err := r.logger.Change(0, GlyphReport, "project", name); err != nil {
				return err
			}

			if err := r.logger.Detailf(
				0, "not declared in the configuration, run with --allow-deletions to delete it",
			); err != nil {
				return err
			}

			r.summary.Reported++
		default:
			if err := r.logger.Change(0, GlyphDelete, "project", name); err != nil {
				return err
			}

			if err := r.deleteProject(ctx, name); err != nil {
				return fmt.Errorf("deleting project %s: %w", name, err)
			}

			r.summary.ProjectsDeleted++
		}
	}

	return nil
}

// subprojectPolicy returns the policy governing an undeclared project, which
// is the one declared by its nearest declared ancestor.
func (r *reconciler) subprojectPolicy(name string) config.Policy {
	for _, ancestor := range config.Ancestors(name) {
		if project, ok := r.declared[ancestor]; ok {
			return project.Subprojects.PolicyOrDefault()
		}
	}

	return config.PolicyIgnore
}

// undeclaredProjects enumerates every project below a prune root that the
// configuration does not declare.
func (r *reconciler) undeclaredProjects(
	ctx context.Context,
	projects []*config.Project,
) ([]string, error) {
	var undeclared []string

	seen := make(map[string]struct{})

	for _, root := range pruneRoots(projects) {
		names, err := r.client.ListSubprojects(ctx, root)
		if err != nil {
			return nil, fmt.Errorf("listing subprojects of %s: %w", root, err)
		}

		for _, name := range names {
			if _, ok := r.declared[name]; ok {
				continue
			}

			if _, ok := seen[name]; ok {
				continue
			}

			seen[name] = struct{}{}

			undeclared = append(undeclared, name)
		}
	}

	return undeclared, nil
}

// pruneRoots returns the projects OBS has to be searched below. A project
// qualifies when it looks at its subprojects at all and no declared ancestor
// already does, because the search matches the whole subtree and enumerating
// it once is enough.
func pruneRoots(projects []*config.Project) []string {
	looking := make(map[string]struct{}, len(projects))

	for _, project := range projects {
		if project.Subprojects.PolicyOrDefault() != config.PolicyIgnore {
			looking[project.Name] = struct{}{}
		}
	}

	return topmost(looking)
}

// topmost reduces a set of project names to those that have no ancestor in the
// same set, sorted by name.
func topmost(names map[string]struct{}) []string {
	roots := make([]string, 0, len(names))

	for name := range names {
		covered := false

		for _, ancestor := range config.Ancestors(name) {
			if _, ok := names[ancestor]; ok {
				covered = true

				break
			}
		}

		if !covered {
			roots = append(roots, name)
		}
	}

	sort.Strings(roots)

	return roots
}

// sortByHierarchy orders projects so that a parent always comes before its
// children, which is the order OBS needs them to be created in.
func sortByHierarchy(projects []*config.Project) {
	sort.SliceStable(projects, func(i, j int) bool {
		if projects[i].Depth() != projects[j].Depth() {
			return projects[i].Depth() < projects[j].Depth()
		}

		return projects[i].Name < projects[j].Name
	})
}

// sortByDependencies orders metadata updates by repository paths and release
// targets within the declared set. The input is already in hierarchy order,
// which is retained when there are no dependencies.
func sortByDependencies(projects []*config.Project) ([]*config.Project, error) {
	declared := make(map[string]*config.Project, len(projects))
	for _, project := range projects {
		declared[project.Name] = project
	}

	ordered := make([]*config.Project, 0, len(projects))
	visited := make(map[string]bool, len(projects))
	active := make(map[string]int, len(projects))

	var stack []string

	var visit func(*config.Project) error

	visit = func(project *config.Project) error {
		if visited[project.Name] {
			return nil
		}

		if start, exists := active[project.Name]; exists {
			cycle := append(append([]string(nil), stack[start:]...), project.Name)

			return fmt.Errorf("project metadata dependency cycle: %s", strings.Join(cycle, " -> "))
		}

		active[project.Name] = len(stack)
		stack = append(stack, project.Name)

		var dependencies []string

		for _, repository := range project.Repositories {
			for _, path := range repository.Paths {
				dependencies = append(dependencies, path.Project)
			}

			for _, target := range repository.ReleaseTargets {
				dependencies = append(dependencies, target.Project)
			}
		}

		sort.Strings(dependencies)

		for _, name := range dependencies {
			// Same-project repository targets were prepared before metadata
			// updates. Undeclared projects remain outside reconciliation.
			if name == project.Name {
				continue
			}

			dependency, exists := declared[name]
			if !exists {
				continue
			}

			if err := visit(dependency); err != nil {
				return err
			}
		}

		stack = stack[:len(stack)-1]

		delete(active, project.Name)
		visited[project.Name] = true
		ordered = append(ordered, project)

		return nil
	}

	for _, project := range projects {
		if err := visit(project); err != nil {
			return nil, err
		}
	}

	return ordered, nil
}

// putProjectMeta sends a project mutation only during a confirmed run.
func (r *reconciler) putProjectMeta(ctx context.Context, project *obs.Project) error {
	if !r.options.Confirm {
		return nil
	}

	return r.client.PutProjectMeta(ctx, project)
}

// putPackageMeta sends a package mutation only during a confirmed run.
func (r *reconciler) putPackageMeta(ctx context.Context, pkg *obs.Package) error {
	if !r.options.Confirm {
		return nil
	}

	return r.client.PutPackageMeta(ctx, pkg)
}

// deleteProject sends a project deletion only during a confirmed run.
func (r *reconciler) deleteProject(ctx context.Context, name string) error {
	if !r.options.Confirm {
		return nil
	}

	return r.client.DeleteProject(ctx, name)
}

// deletePackage sends a package deletion only during a confirmed run.
func (r *reconciler) deletePackage(ctx context.Context, projectName, packageName string) error {
	if !r.options.Confirm {
		return nil
	}

	return r.client.DeletePackage(ctx, projectName, packageName)
}
