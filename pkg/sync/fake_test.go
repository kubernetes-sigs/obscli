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
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"sigs.k8s.io/release-sdk/obs"

	"sigs.k8s.io/obscli/pkg/config"
)

// fakeClient is an in memory OBS instance. It records every call, so that a
// test can assert both the resulting state and the requests that produced it.
type fakeClient struct {
	projects    map[string]*obs.Project
	packages    map[string]map[string]*obs.Package
	subprojects map[string][]string

	// calls records the mutating and listing calls in the order they happened.
	calls []string

	// reads records the projects whose meta was requested, so that a test can
	// assert which projects were looked at all.
	reads []string

	// mutationErr makes every mutating call fail when set.
	mutationErr error

	projectWrites   []*obs.Project
	projectWriteErr func(*obs.Project) error
	packageWriteErr error

	// listPackagesErr is returned by ListPackages when set.
	listPackagesErr error
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		projects:    map[string]*obs.Project{},
		packages:    map[string]map[string]*obs.Package{},
		subprojects: map[string][]string{},
	}
}

func (c *fakeClient) GetProjectMeta(_ context.Context, name string) (*obs.Project, error) {
	c.reads = append(c.reads, name)

	project, ok := c.projects[name]
	if !ok {
		return nil, notFound()
	}

	// A nil entry simulates a client that returns no metadata at all.
	if project == nil {
		return nil, nil //nolint:nilnil // Exercises the reconciler's handling of missing metadata.
	}

	copied := *project

	return &copied, nil
}

func (c *fakeClient) PutProjectMeta(_ context.Context, project *obs.Project) error {
	c.calls = append(c.calls, "put project "+project.Name)
	c.projectWrites = append(c.projectWrites, NormalizeProject(project))

	if c.mutationErr != nil {
		return c.mutationErr
	}

	if c.projectWriteErr != nil {
		if err := c.projectWriteErr(project); err != nil {
			return err
		}
	}

	available := NormalizeProject(c.projects[project.Name])
	if available == nil {
		available = MinimalProjectMeta(project.Name)
	}

	// OBS validates release targets before saving each repository in its
	// first pass. Paths are resolved only after all repositories are saved.
	for i := range project.Repositories {
		repository := &project.Repositories[i]
		for _, target := range repository.ReleaseTargets {
			if target.Project == project.Name && target.Repository == repository.Name {
				return fmt.Errorf("repository %s/%s releases into itself", project.Name, repository.Name)
			}

			if err := c.checkRepositoryReference(available, target.Project, target.Repository); err != nil {
				return err
			}
		}

		index := slices.IndexFunc(available.Repositories, func(existing obs.Repository) bool {
			return existing.Name == repository.Name
		})
		if index < 0 {
			available.Repositories = append(available.Repositories, *repository)
		} else {
			available.Repositories[index] = *repository
		}
	}

	for i := range project.Repositories {
		repository := &project.Repositories[i]
		for _, path := range repository.Paths {
			if path.Project == project.Name && path.Repository == repository.Name {
				return fmt.Errorf("repository %s/%s references itself", project.Name, repository.Name)
			}

			if err := c.checkRepositoryReference(available, path.Project, path.Repository); err != nil {
				return err
			}
		}
	}

	if before := c.projects[project.Name]; before != nil {
		for i := range before.Repositories {
			name := before.Repositories[i].Name
			if hasRepository(project.Repositories, name) {
				continue
			}

			if err := c.checkRepositoryRemoval(available, name); err != nil {
				return err
			}

			available.Repositories = slices.DeleteFunc(available.Repositories, func(existing obs.Repository) bool {
				return existing.Name == name
			})
		}
	}

	copied := *project
	c.projects[project.Name] = &copied

	return nil
}

func (c *fakeClient) DeleteProject(_ context.Context, name string) error {
	c.calls = append(c.calls, "delete project "+name)

	if c.mutationErr != nil {
		return c.mutationErr
	}

	delete(c.projects, name)

	return nil
}

func (c *fakeClient) ListPackages(_ context.Context, projectName string) ([]string, error) {
	if c.listPackagesErr != nil {
		return nil, c.listPackagesErr
	}

	c.calls = append(c.calls, "list packages "+projectName)

	names := make([]string, 0, len(c.packages[projectName]))
	for name := range c.packages[projectName] {
		names = append(names, name)
	}

	sort.Strings(names)

	return names, nil
}

func (c *fakeClient) ListSubprojects(_ context.Context, projectName string) ([]string, error) {
	c.calls = append(c.calls, "list subprojects "+projectName)

	return c.subprojects[projectName], nil
}

func (c *fakeClient) GetPackageMeta(_ context.Context, projectName, packageName string) (*obs.Package, error) {
	pkg, ok := c.packages[projectName][packageName]
	if !ok {
		return nil, notFound()
	}

	copied := *pkg

	return &copied, nil
}

func (c *fakeClient) PutPackageMeta(_ context.Context, pkg *obs.Package) error {
	projectName := pkg.Project

	c.calls = append(c.calls, fmt.Sprintf("put package %s/%s", projectName, pkg.Name))

	if c.mutationErr != nil {
		return c.mutationErr
	}

	if c.packageWriteErr != nil {
		return c.packageWriteErr
	}

	if c.packages[projectName] == nil {
		c.packages[projectName] = map[string]*obs.Package{}
	}

	copied := *pkg
	c.packages[projectName][pkg.Name] = &copied

	return nil
}

func (c *fakeClient) DeletePackage(_ context.Context, projectName, packageName string) error {
	c.calls = append(c.calls, fmt.Sprintf("delete package %s/%s", projectName, packageName))

	if c.mutationErr != nil {
		return c.mutationErr
	}

	delete(c.packages[projectName], packageName)

	return nil
}

func (c *fakeClient) checkRepositoryReference(updating *obs.Project, projectName, repositoryName string) error {
	project := c.projects[projectName]
	if projectName == updating.Name {
		project = updating
	}

	if project != nil && hasRepository(project.Repositories, repositoryName) {
		return nil
	}

	return fmt.Errorf("repository %s/%s does not exist", projectName, repositoryName)
}

func (c *fakeClient) checkRepositoryRemoval(updating *obs.Project, repositoryName string) error {
	for name, project := range c.projects {
		if name == updating.Name {
			project = updating
		}

		for i := range project.Repositories {
			repository := &project.Repositories[i]
			for _, path := range repository.Paths {
				if path.Project == updating.Name && path.Repository == repositoryName {
					return fmt.Errorf("repository %s/%s is still referenced by %s/%s",
						updating.Name, repositoryName, name, repository.Name)
				}
			}

			for _, target := range repository.ReleaseTargets {
				if target.Project == updating.Name && target.Repository == repositoryName {
					return fmt.Errorf("repository %s/%s is still a release target of %s/%s",
						updating.Name, repositoryName, name, repository.Name)
				}
			}
		}
	}

	return nil
}

// addProject registers a live project.
func (c *fakeClient) addProject(project *obs.Project) {
	c.projects[project.Name] = project
}

// addPackages registers live packages with the minimal meta.
func (c *fakeClient) addPackages(projectName string, names ...string) {
	if c.packages[projectName] == nil {
		c.packages[projectName] = map[string]*obs.Package{}
	}

	for _, name := range names {
		c.packages[projectName][name] = &obs.Package{Name: name, Project: projectName}
	}
}

// mutatingCalls returns the recorded calls without the read only ones.
func (c *fakeClient) mutatingCalls() []string {
	var calls []string

	for _, call := range c.calls {
		if strings.HasPrefix(call, "put ") || strings.HasPrefix(call, "delete ") {
			calls = append(calls, call)
		}
	}

	return calls
}

// notFound builds the error the OBS client returns for a missing resource.
func notFound() error {
	return &obs.APIError{
		HTTPStatusCode: http.StatusNotFound,
		OBSStatusCode:  obs.StatusCodeUnknownProject,
		Message:        "not found",
	}
}

// buildConfig turns projects into a configuration, giving each a source path.
func buildConfig(t *testing.T, yamlDocuments ...string) *config.Config {
	t.Helper()

	dir := t.TempDir()
	projects := make([]*config.Project, 0, len(yamlDocuments))

	for i, document := range yamlDocuments {
		path := filepath.Join(dir, fmt.Sprintf("project-%d.yaml", i))
		require.NoError(t, os.WriteFile(path, []byte(document), 0o600))

		project, err := config.LoadFile(path)
		require.NoError(t, err)

		projects = append(projects, project)
	}

	return &config.Config{Projects: projects}
}

// loadFixtureProject decodes an OBS meta document from testdata.
func loadFixtureProject(t *testing.T, name string) *obs.Project {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)

	project := &obs.Project{}
	require.NoError(t, xml.Unmarshal(content, project))

	return project
}

// loadFixtureConfig loads a configuration document from testdata.
func loadFixtureConfig(t *testing.T, name string) *config.Project {
	t.Helper()

	project, err := config.LoadFile(filepath.Join("testdata", name))
	require.NoError(t, err)

	return project
}
