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

package export

import (
	"context"
	"sort"

	"sigs.k8s.io/release-sdk/obs"
)

// fakeClient is an in memory OBS instance holding the projects and packages an
// export is expected to read. It records every write, so that the round trip
// test can prove that applying an export touches nothing.
type fakeClient struct {
	projects    map[string]*obs.Project
	packages    map[string]map[string]*obs.Package
	subprojects map[string][]string

	// writes records the mutating calls in the order they happened.
	writes []string
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		projects:    map[string]*obs.Project{},
		packages:    map[string]map[string]*obs.Package{},
		subprojects: map[string][]string{},
	}
}

func (c *fakeClient) GetProjectMeta(_ context.Context, name string) (*obs.Project, error) {
	project, ok := c.projects[name]
	if !ok {
		return nil, notFound(obs.StatusCodeUnknownProject)
	}

	copied := *project

	return &copied, nil
}

func (c *fakeClient) ListSubprojects(_ context.Context, projectName string) ([]string, error) {
	return c.subprojects[projectName], nil
}

func (c *fakeClient) ListPackages(_ context.Context, projectName string) ([]string, error) {
	names := make([]string, 0, len(c.packages[projectName]))
	for name := range c.packages[projectName] {
		names = append(names, name)
	}

	sort.Strings(names)

	return names, nil
}

func (c *fakeClient) GetPackageMeta(_ context.Context, projectName, packageName string) (*obs.Package, error) {
	pkg, ok := c.packages[projectName][packageName]
	if !ok {
		return nil, notFound(obs.StatusCodeUnknownPackage)
	}

	copied := *pkg

	return &copied, nil
}

// PutProjectMeta records a project write.
func (c *fakeClient) PutProjectMeta(_ context.Context, project *obs.Project) error {
	c.writes = append(c.writes, "put project "+project.Name)

	return nil
}

// DeleteProject records a project deletion.
func (c *fakeClient) DeleteProject(_ context.Context, name string) error {
	c.writes = append(c.writes, "delete project "+name)

	return nil
}

// PutPackageMeta records a package write.
func (c *fakeClient) PutPackageMeta(_ context.Context, pkg *obs.Package) error {
	c.writes = append(c.writes, "put package "+pkg.Project+"/"+pkg.Name)

	return nil
}

// DeletePackage records a package deletion.
func (c *fakeClient) DeletePackage(_ context.Context, projectName, packageName string) error {
	c.writes = append(c.writes, "delete package "+projectName+"/"+packageName)

	return nil
}

// addProject registers a live project, recording it as a subproject of every
// ancestor so that a recursive export finds it.
func (c *fakeClient) addProject(project *obs.Project) {
	c.projects[project.Name] = project

	for _, ancestor := range ancestors(project.Name) {
		c.subprojects[ancestor] = append(c.subprojects[ancestor], project.Name)
	}
}

// addPackage registers a live package.
func (c *fakeClient) addPackage(projectName string, pkg *obs.Package) {
	if c.packages[projectName] == nil {
		c.packages[projectName] = map[string]*obs.Package{}
	}

	c.packages[projectName][pkg.Name] = pkg
}

// ancestors returns the projects a project name is listed as a subproject of.
func ancestors(name string) []string {
	var result []string

	for index, char := range name {
		if char == ':' {
			result = append(result, name[:index])
		}
	}

	return result
}

// notFound builds the error the OBS client returns for a missing resource.
func notFound(code string) error {
	return &obs.APIError{HTTPStatusCode: 404, OBSStatusCode: code, Message: "not found"}
}
