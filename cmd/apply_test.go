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

package cmd

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/release-sdk/obs"
)

func TestApplyMutationGates(t *testing.T) {
	t.Setenv(usernameEnv, "owner")
	t.Setenv(passwordEnv, "not-a-real-password")
	path := writeTestConfig(t, t.TempDir(), "project.yaml", `name: isv:kubernetes
title: Updated
maintainers: [owner]
packages:
  policy: prune
  managed:
    - name: kubelet
      title: Updated package
    - name: _patchinfo:example
subprojects:
  policy: prune
`)

	for _, confirm := range []bool{false, true} {
		for _, allowDeletions := range []bool{false, true} {
			t.Run(fmt.Sprintf("confirm=%t/deletions=%t", confirm, allowDeletions), func(t *testing.T) {
				responses := []obsResponse{{
					method: http.MethodGet, path: "/source/isv:kubernetes/_meta", body: exportProjectXML,
				}}
				if confirm {
					responses = append(responses, obsResponse{
						method: http.MethodPut, path: "/source/isv:kubernetes/_meta",
						check: checkProjectBody(t, &obs.Project{
							Name: "isv:kubernetes", Title: "Updated",
							Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
						}),
					})
				}

				responses = append(responses,
					obsResponse{
						method: http.MethodGet, path: "/source/isv:kubernetes",
						body: `<directory><entry name="kubelet"/><entry name="obsolete"/><entry name="kubelet.20260923195931"/></directory>`,
					},
					obsResponse{
						method: http.MethodGet, path: "/source/isv:kubernetes/kubelet/_meta",
						body: `<package name="kubelet" project="isv:kubernetes"><title>Old</title><description></description></package>`,
					},
				)
				if confirm {
					responses = append(responses,
						obsResponse{
							method: http.MethodPut, path: "/source/isv:kubernetes/kubelet/_meta",
							check: checkPackageBody(t, &obs.Package{
								Name: "kubelet", Project: "isv:kubernetes", Title: "Updated package",
							}),
						},
						obsResponse{
							method: http.MethodPut, path: "/source/isv:kubernetes/_patchinfo:example/_meta",
							check: checkPackageBody(t, &obs.Package{Name: "_patchinfo:example", Project: "isv:kubernetes"}),
						},
					)
					if allowDeletions {
						responses = append(responses, obsResponse{
							method: http.MethodDelete, path: "/source/isv:kubernetes/obsolete",
						})
					}
				}

				responses = append(responses, obsResponse{
					method: http.MethodGet, path: "/search/project/id",
					body: `<collection><project name="isv:kubernetes:obsolete"/></collection>`,
					check: func(r *http.Request) {
						assert.Equal(t, "starts-with(@name,'isv:kubernetes:')", r.URL.Query().Get("match"))
					},
				})
				if confirm && allowDeletions {
					responses = append(responses, obsResponse{
						method: http.MethodDelete, path: "/source/isv:kubernetes:obsolete",
					})
				}

				server := newTestOBS(t, responses)

				args := []string{"apply", "--config", path, "--api-url", server.URL}
				if confirm {
					args = append(args, "--confirm")
				}

				if allowDeletions {
					args = append(args, "--allow-deletions")
				}

				output, err := executeTestCommand(t, args...)
				require.NoError(t, err)

				deleted := 0
				if allowDeletions {
					deleted = 1
				} else {
					assert.Contains(t, output, "Reported but left alone: 2")
					assert.Contains(t, output, "! package isv:kubernetes/obsolete")
				}

				assert.Contains(t, output, fmt.Sprintf("Projects: 0 created, 1 updated, %d deleted", deleted))
				assert.Contains(t, output, fmt.Sprintf("Packages: 1 created, 1 updated, %d deleted", deleted))
				assert.Contains(t, output, "+ package isv:kubernetes/_patchinfo:example")
				assert.Contains(t, output, "package isv:kubernetes/kubelet")
				assert.NotContains(t, output, "kubelet.20260923195931")

				if confirm {
					assert.Contains(t, output, "Changes applied.")
					assert.NotContains(t, output, "This was a dry run")
				} else {
					assert.Contains(t, output, "Running in dry run mode, no change is written.")
					assert.Contains(t, output, "This was a dry run, run again with --confirm")
				}
			})
		}
	}
}

func TestApplyValidatesBeforeOBS(t *testing.T) {
	for name, test := range map[string]struct {
		document string
		required string
		wantErr  string
	}{
		"unknown field": {
			document: exportProjectYAML + "typo: true\n",
			wantErr:  "field typo not found",
		},
		"required maintainer": {
			document: exportProjectYAML, required: "another-user",
			wantErr: `must have "another-user" as a maintainer`,
		},
		"missing declared repository": {
			document: exportProjectYAML + `repositories:
  - name: rpm
    paths:
      - project: isv:kubernetes
        repository: missing
`,
			wantErr: `refers to repository "missing", which is not declared in project "isv:kubernetes"`,
		},
	} {
		for _, credentials := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/credentials=%t", name, credentials), func(t *testing.T) {
				t.Setenv(usernameEnv, "")
				t.Setenv(passwordEnv, "")

				if credentials {
					t.Setenv(usernameEnv, "owner")
					t.Setenv(passwordEnv, "not-a-real-password")
				}

				server := newTestOBS(t, nil)
				path := writeTestConfig(t, t.TempDir(), "project.yaml", test.document)

				args := []string{"apply", "--config", path, "--api-url", server.URL, "--confirm", "--allow-deletions"}
				if test.required != "" {
					args = append(args, "--required-maintainers", test.required)
				}

				output, err := executeTestCommand(t, args...)
				require.ErrorContains(t, err, test.wantErr)
				assert.NotContains(t, err.Error(), "OBS credentials are missing")
				assert.Empty(t, output)
			})
		}
	}
}

func TestApplyRejectsMismatchedProjectMetadata(t *testing.T) {
	t.Setenv(usernameEnv, "owner")
	t.Setenv(passwordEnv, "not-a-real-password")
	path := writeTestConfig(t, t.TempDir(), "project.yaml", `name: isv:kubernetes
maintainers: [owner]
repositories:
  - name: new
    architectures: [x86_64]
`)

	for name, test := range map[string]struct {
		body    string
		wantErr string
	}{
		"missing name": {
			body:    `<project><title></title><description></description></project>`,
			wantErr: `OBS returned metadata for ""`,
		},
		"unrelated project": {
			body:    `<project name="home:unrelated"><title></title><description></description></project>`,
			wantErr: `OBS returned metadata for "home:unrelated"`,
		},
	} {
		for mode, confirm := range map[string]bool{"dry run": false, "confirmed": true} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				server := newTestOBS(t, []obsResponse{{
					method: http.MethodGet, path: "/source/isv:kubernetes/_meta", body: test.body,
				}})

				args := []string{"apply", "--config", path, "--api-url", server.URL, "--allow-deletions"}
				if confirm {
					args = append(args, "--confirm")
				}

				output, err := executeTestCommand(t, args...)
				require.ErrorContains(t, err, test.wantErr)
				assert.Contains(t, err.Error(), "loading the live state of project isv:kubernetes")
				assert.Contains(t, output, "Projects: 0 created, 0 updated, 0 deleted")
				assert.Contains(t, output, "Packages: 0 created, 0 updated, 0 deleted")
				assert.Contains(t, output, "The run stopped early")
				assert.NotContains(t, output, "Changes applied.")
				assert.NotContains(t, output, "Everything is up to date.")
			})
		}
	}
}

func TestApplyReportsPartialFailure(t *testing.T) {
	t.Setenv(usernameEnv, "owner")
	t.Setenv(passwordEnv, "not-a-real-password")
	path := writeTestConfig(t, t.TempDir(), "project.yaml",
		"name: isv:kubernetes\ntitle: Updated\nmaintainers: [owner]\n")

	server := newTestOBS(t, []obsResponse{
		{method: http.MethodGet, path: "/source/isv:kubernetes/_meta", body: exportProjectXML},
		{method: http.MethodPut, path: "/source/isv:kubernetes/_meta"},
		{
			method: http.MethodGet, path: "/source/isv:kubernetes", status: http.StatusInternalServerError,
			body: `<status code="internal_error"><summary>Listing failed</summary></status>`,
		},
	})

	output, err := executeTestCommand(t, "apply", "--config", path, "--api-url", server.URL, "--confirm")
	require.ErrorContains(t, err, "Listing failed")
	assert.Contains(t, output, "Projects: 0 created, 1 updated, 0 deleted")
	assert.Contains(t, output, "Packages: 0 created, 0 updated, 0 deleted")
	assert.Contains(t, output, "The run stopped early")
	assert.NotContains(t, output, "Changes applied.")
	assert.NotContains(t, output, "Everything is up to date.")
}

func TestApplyReportsRejectedMetadata(t *testing.T) {
	t.Setenv(usernameEnv, "owner")
	t.Setenv(passwordEnv, "not-a-real-password")
	path := writeTestConfig(t, t.TempDir(), "project.yaml", exportProjectYAML+`repositories:
  - name: rpm
    paths:
      - project: external:base
        repository: missing
`)
	server := newTestOBS(t, []obsResponse{
		{method: http.MethodGet, path: "/source/isv:kubernetes/_meta", body: exportProjectXML},
		{
			method: http.MethodPut, path: "/source/isv:kubernetes/_meta",
			check: checkProjectBody(t, &obs.Project{
				Name: "isv:kubernetes", Title: "Kubernetes",
				Persons:      []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
				Repositories: []obs.Repository{{Name: "rpm"}},
			}),
		},
		{
			method: http.MethodPut, path: "/source/isv:kubernetes/_meta", status: http.StatusBadRequest,
			body: `<status code="unknown_repository"><summary>Repository external:base/missing does not exist</summary></status>`,
			check: checkProjectBody(t, &obs.Project{
				Name: "isv:kubernetes", Title: "Kubernetes",
				Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
				Repositories: []obs.Repository{{
					Name: "rpm", Paths: []obs.RepositoryPath{{Project: "external:base", Repository: "missing"}},
				}},
			}),
		},
	})

	output, err := executeTestCommand(t, "apply", "--config", path, "--api-url", server.URL, "--confirm")
	require.ErrorContains(t, err, "Repository external:base/missing does not exist")
	assert.Contains(t, output, "Projects: 0 created, 1 updated, 0 deleted")
	assert.Contains(t, output, "Packages: 0 created, 0 updated, 0 deleted")
	assert.Contains(t, output, "The run stopped early")
	assert.NotContains(t, output, "Changes applied.")
	assert.NotContains(t, output, "Everything is up to date.")
}

func TestApplyCreatesRepositoryTargetsBeforeReferences(t *testing.T) {
	t.Setenv(usernameEnv, "owner")
	t.Setenv(passwordEnv, "not-a-real-password")
	dir := t.TempDir()
	writeTestConfig(t, dir, "a.yaml", `name: isv:kubernetes:a
maintainers: [owner]
subprojects: {policy: ignore}
repositories:
  - name: rpm
    paths:
      - project: isv:kubernetes:z
        repository: rpm
    releaseTargets:
      - project: isv:kubernetes:z
        repository: rpm
`)
	writeTestConfig(t, dir, "z.yaml", `name: isv:kubernetes:z
maintainers: [owner]
subprojects: {policy: ignore}
repositories:
  - name: rpm
`)

	responses := make([]obsResponse, 0, 8)
	for _, name := range []string{"isv:kubernetes:a", "isv:kubernetes:z"} {
		responses = append(responses, obsResponse{
			method: http.MethodGet, path: "/source/" + name + "/_meta", status: http.StatusNotFound,
			body: `<status code="unknown_project"><summary>Project not found</summary></status>`,
		})
	}

	for _, name := range []string{"isv:kubernetes:a", "isv:kubernetes:z"} {
		responses = append(responses, obsResponse{
			method: http.MethodPut, path: "/source/" + name + "/_meta",
			check: checkProjectBody(t, &obs.Project{Name: name}),
		})
	}

	for _, name := range []string{"isv:kubernetes:a", "isv:kubernetes:z"} {
		responses = append(responses, obsResponse{
			method: http.MethodPut, path: "/source/" + name + "/_meta",
			check: checkProjectBody(t, &obs.Project{
				Name: name, Repositories: []obs.Repository{{Name: "rpm"}},
			}),
		})
	}

	responses = append(responses,
		obsResponse{
			method: http.MethodPut, path: "/source/isv:kubernetes:z/_meta",
			check: checkProjectBody(t, &obs.Project{
				Name: "isv:kubernetes:z", Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
				Repositories: []obs.Repository{{Name: "rpm"}},
			}),
		},
		obsResponse{
			method: http.MethodPut, path: "/source/isv:kubernetes:a/_meta",
			check: checkProjectBody(t, &obs.Project{
				Name: "isv:kubernetes:a", Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
				Repositories: []obs.Repository{{
					Name:           "rpm",
					Paths:          []obs.RepositoryPath{{Project: "isv:kubernetes:z", Repository: "rpm"}},
					ReleaseTargets: []obs.ReleaseTarget{{Project: "isv:kubernetes:z", Repository: "rpm"}},
				}},
			}),
		},
	)
	server := newTestOBS(t, responses)

	output, err := executeTestCommand(t, "apply", "--config", dir, "--api-url", server.URL, "--confirm")
	require.NoError(t, err)
	assert.Contains(t, output, "Projects: 2 created, 0 updated, 0 deleted")
	assert.Contains(t, output, "Changes applied.")
}

func TestApplyPreparesSameProjectReleaseTarget(t *testing.T) {
	t.Setenv(usernameEnv, "owner")
	t.Setenv(passwordEnv, "not-a-real-password")
	path := writeTestConfig(t, t.TempDir(), "project.yaml", `name: isv:kubernetes
maintainers: [owner]
subprojects: {policy: ignore}
repositories:
  - name: build
    releaseTargets:
      - project: isv:kubernetes
        repository: release
  - name: release
`)
	server := newTestOBS(t, []obsResponse{
		{
			method: http.MethodGet, path: "/source/isv:kubernetes/_meta", status: http.StatusNotFound,
			body: `<status code="unknown_project"><summary>Project not found</summary></status>`,
		},
		{
			method: http.MethodPut, path: "/source/isv:kubernetes/_meta",
			check: checkProjectBody(t, &obs.Project{Name: "isv:kubernetes"}),
		},
		{
			method: http.MethodPut, path: "/source/isv:kubernetes/_meta",
			check: checkProjectBody(t, &obs.Project{
				Name: "isv:kubernetes", Repositories: []obs.Repository{{Name: "build"}, {Name: "release"}},
			}),
		},
		{
			method: http.MethodPut, path: "/source/isv:kubernetes/_meta",
			check: checkProjectBody(t, &obs.Project{
				Name: "isv:kubernetes", Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
				Repositories: []obs.Repository{
					{Name: "build", ReleaseTargets: []obs.ReleaseTarget{{Project: "isv:kubernetes", Repository: "release"}}},
					{Name: "release"},
				},
			}),
		},
	})

	output, err := executeTestCommand(t, "apply", "--config", path, "--api-url", server.URL, "--confirm")
	require.NoError(t, err)
	assert.Contains(t, output, "Projects: 1 created, 0 updated, 0 deleted")
	assert.Contains(t, output, "Changes applied.")
}

func TestApplyStagesRepositoryReplacement(t *testing.T) {
	t.Setenv(usernameEnv, "owner")
	t.Setenv(passwordEnv, "not-a-real-password")
	dir := t.TempDir()
	writeTestConfig(t, dir, "consumer.yaml", `name: isv:kubernetes:consumer
maintainers: [owner]
subprojects: {policy: ignore}
repositories:
  - name: rpm
    paths:
      - project: isv:kubernetes:target
        repository: new
`)
	writeTestConfig(t, dir, "target.yaml", `name: isv:kubernetes:target
maintainers: [owner]
subprojects: {policy: ignore}
repositories:
  - name: new
`)

	owner := []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}}
	server := newTestOBS(t, []obsResponse{
		{
			method: http.MethodGet, path: "/source/isv:kubernetes:consumer/_meta",
			body: `<project name="isv:kubernetes:consumer"><title></title><description></description>` +
				`<person userid="owner" role="maintainer"/><repository name="rpm">` +
				`<path project="isv:kubernetes:target" repository="old"/></repository></project>`,
		},
		{
			method: http.MethodGet, path: "/source/isv:kubernetes:target/_meta",
			body: `<project name="isv:kubernetes:target"><title></title><description></description>` +
				`<person userid="owner" role="maintainer"/><repository name="old"/></project>`,
		},
		{
			method: http.MethodPut, path: "/source/isv:kubernetes:target/_meta",
			check: checkProjectBody(t, &obs.Project{
				Name: "isv:kubernetes:target", Persons: owner,
				Repositories: []obs.Repository{{Name: "old"}, {Name: "new"}},
			}),
		},
		{
			method: http.MethodPut, path: "/source/isv:kubernetes:target/_meta",
			check: checkProjectBody(t, &obs.Project{
				Name: "isv:kubernetes:target", Persons: owner,
				Repositories: []obs.Repository{{Name: "new"}, {Name: "old"}},
			}),
		},
		{method: http.MethodGet, path: "/source/isv:kubernetes:target", body: `<directory/>`},
		{
			method: http.MethodPut, path: "/source/isv:kubernetes:consumer/_meta",
			check: checkProjectBody(t, &obs.Project{
				Name: "isv:kubernetes:consumer", Persons: owner,
				Repositories: []obs.Repository{{
					Name: "rpm", Paths: []obs.RepositoryPath{{Project: "isv:kubernetes:target", Repository: "new"}},
				}},
			}),
		},
		{method: http.MethodGet, path: "/source/isv:kubernetes:consumer", body: `<directory/>`},
		{
			method: http.MethodPut, path: "/source/isv:kubernetes:target/_meta",
			check: func(r *http.Request) {
				assert.Empty(t, r.URL.Query())
				checkProjectBody(t, &obs.Project{
					Name: "isv:kubernetes:target", Persons: owner,
					Repositories: []obs.Repository{{Name: "new"}},
				})(r)
			},
		},
	})

	output, err := executeTestCommand(t, "apply", "--config", dir, "--api-url", server.URL, "--confirm")
	require.NoError(t, err)
	assert.Contains(t, output, "Projects: 0 created, 2 updated, 0 deleted")
	assert.Contains(t, output, "Packages: 0 created, 0 updated, 0 deleted")
	assert.Contains(t, output, "Changes applied.")
}
