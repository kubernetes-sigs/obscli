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
	"strings"

	"sigs.k8s.io/release-sdk/obs"
)

// Policy controls how a resource that exists in OBS but is not declared in
// the configuration is treated. It applies to the unmanaged packages of a
// project (`packages.policy`) and to its undeclared subprojects
// (`subprojects.policy`).
type Policy string

const (
	// PolicyIgnore never reports and never touches the undeclared resource.
	// It is meant for release projects, whose contents are machine-managed.
	PolicyIgnore Policy = "ignore"

	// PolicyReport reports the undeclared resource but never deletes it. It is
	// the default.
	PolicyReport Policy = "report"

	// PolicyPrune deletes the undeclared resource. Deletion additionally
	// requires --confirm and --allow-deletions.
	PolicyPrune Policy = "prune"
)

// Policies lists every valid policy.
var Policies = []Policy{
	PolicyIgnore,
	PolicyReport,
	PolicyPrune,
}

// Config is the complete desired state, loaded from one or more YAML files.
type Config struct {
	// Projects are the declared projects, in load order.
	Projects []*Project
}

// Project is the desired state of a single OBS project. It is the content of
// exactly one YAML document.
type Project struct {
	// Name is the authoritative project identity, such as
	// isv:kubernetes:core:stable:v1.34:build. The file layout carries no
	// meaning.
	Name string `yaml:"name"`

	// Kind is the OBS project kind. Empty means a standard project.
	Kind obs.ProjectKind `yaml:"kind,omitempty"`

	Title       string `yaml:"title,omitempty"`
	Description string `yaml:"description,omitempty"`
	URL         string `yaml:"url,omitempty"`

	// People assigns roles to OBS users.
	People People `yaml:",inline"`

	// Groups assigns roles to OBS groups. obscli never creates groups nor
	// manages their membership.
	Groups People `yaml:"groups,omitempty"`

	// Flags are the project wide OBS flags.
	Flags Flags `yaml:"flags,omitempty"`

	// Repositories are the build repositories, in a meaningful order.
	Repositories []Repository `yaml:"repositories,omitempty"`

	// Packages declares the managed packages and how to treat the others.
	Packages Packages `yaml:"packages,omitempty"`

	// Subprojects declares how to treat projects that live below this one in
	// OBS but are not declared anywhere in the configuration.
	Subprojects Subprojects `yaml:"subprojects,omitempty"`

	// sourcePath is the file the project was loaded from. It is only used to
	// produce helpful error messages.
	sourcePath string
}

// SourcePath returns the file the project was loaded from.
func (p *Project) SourcePath() string {
	return p.sourcePath
}

// Depth is the number of colon separated segments of the project name. It
// orders projects so that parents are always created before their children.
func (p *Project) Depth() int {
	return strings.Count(p.Name, ":")
}

// Parent returns the name of the parent project and whether there is one.
func (p *Project) Parent() (string, bool) {
	index := strings.LastIndex(p.Name, ":")
	if index < 0 {
		return "", false
	}

	return p.Name[:index], true
}

// People assigns OBS roles to subjects, which are either users or groups. Each
// field holds the subjects of a single role.
type People struct {
	Maintainers []string `yaml:"maintainers,omitempty"`
	Bugowners   []string `yaml:"bugowners,omitempty"`
	Reviewers   []string `yaml:"reviewers,omitempty"`
	Readers     []string `yaml:"readers,omitempty"`
	Downloaders []string `yaml:"downloaders,omitempty"`
}

// ByRole returns the subjects of every non-empty role, keyed by role.
func (p *People) ByRole() map[obs.Role][]string {
	roles := map[obs.Role][]string{
		obs.RoleMaintainer: p.Maintainers,
		obs.RoleBugOwner:   p.Bugowners,
		obs.RoleReviewer:   p.Reviewers,
		obs.RoleReader:     p.Readers,
		obs.RoleDownloader: p.Downloaders,
	}

	for role, subjects := range roles {
		if len(subjects) == 0 {
			delete(roles, role)
		}
	}

	return roles
}

// IsEmpty reports whether no role has any subject.
func (p *People) IsEmpty() bool {
	return len(p.Maintainers) == 0 &&
		len(p.Bugowners) == 0 &&
		len(p.Reviewers) == 0 &&
		len(p.Readers) == 0 &&
		len(p.Downloaders) == 0
}

// Repository is a single OBS build repository of a project.
type Repository struct {
	Name string `yaml:"name"`

	// Architectures are the build architectures, in a meaningful order.
	Architectures []string `yaml:"architectures,omitempty"`

	// Paths are the repositories this repository builds against, in a
	// meaningful order.
	Paths []RepositoryPath `yaml:"paths,omitempty"`

	// ReleaseTargets are the targets packages are released to. They are only
	// used on builder projects.
	ReleaseTargets []ReleaseTarget `yaml:"releaseTargets,omitempty"`
}

// RepositoryPath is a repository of another project this repository builds
// against.
type RepositoryPath struct {
	Project    string `yaml:"project"`
	Repository string `yaml:"repository"`
}

// ReleaseTarget is the repository built packages are released to.
type ReleaseTarget struct {
	Project    string `yaml:"project"`
	Repository string `yaml:"repository"`

	// Trigger is when the release happens. Empty means the OBS default.
	Trigger obs.ReleaseTrigger `yaml:"trigger,omitempty"`
}

// Packages declares the managed packages of a project and how packages that
// are not declared are treated.
type Packages struct {
	// Policy defaults to PolicyReport.
	Policy Policy `yaml:"policy,omitempty"`

	// Managed are the packages obscli owns. The declared fields are the
	// complete desired meta of the package.
	Managed []Package `yaml:"managed,omitempty"`
}

// PolicyOrDefault returns the package policy, defaulting to
// PolicyReport when it is not set.
func (p Packages) PolicyOrDefault() Policy {
	if p.Policy == "" {
		return PolicyReport
	}

	return p.Policy
}

// Subprojects declares how a project treats the projects that live below it in
// OBS but are not declared anywhere in the configuration. It is the project
// level counterpart of Packages: an undeclared project has no file of its own,
// so the policy governing it lives on its nearest declared ancestor.
type Subprojects struct {
	// Policy defaults to PolicyReport.
	Policy Policy `yaml:"policy,omitempty"`
}

// PolicyOrDefault returns the subproject policy, defaulting to PolicyReport
// when it is not set.
func (s Subprojects) PolicyOrDefault() Policy {
	if s.Policy == "" {
		return PolicyReport
	}

	return s.Policy
}

// Package is the desired state of a single OBS package. An item carrying
// nothing but a name declares the minimal package meta.
type Package struct {
	Name string `yaml:"name"`

	Title       string `yaml:"title,omitempty"`
	Description string `yaml:"description,omitempty"`
	URL         string `yaml:"url,omitempty"`

	// People assigns roles to OBS users.
	People People `yaml:",inline"`

	// Groups assigns roles to OBS groups.
	Groups People `yaml:"groups,omitempty"`

	// Flags are the package wide OBS flags.
	Flags Flags `yaml:"flags,omitempty"`
}
