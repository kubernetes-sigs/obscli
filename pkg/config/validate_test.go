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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/release-sdk/obs"
)

func TestValidateAcceptsTestdata(t *testing.T) {
	t.Parallel()

	config, err := Load(filepath.Join("testdata", "valid"))
	require.NoError(t, err)

	require.NoError(t, Validate(config, ValidateOptions{}))
	require.NoError(t, Validate(config, ValidateOptions{
		RequiredMaintainers: []string{"k8s-release-bot"},
	}))
}

func TestValidateRequiredMaintainers(t *testing.T) {
	t.Parallel()

	config, err := Load(filepath.Join("testdata", "valid"))
	require.NoError(t, err)

	err = Validate(config, ValidateOptions{RequiredMaintainers: []string{"someone-else"}})
	require.ErrorContains(t, err, `must have "someone-else" as a maintainer`)
}

func TestValidateInheritsMaintainersFromAncestors(t *testing.T) {
	t.Parallel()

	t.Run("direct ancestor", func(t *testing.T) {
		t.Parallel()

		config := newConfig(
			&Project{Name: "isv:kubernetes", People: People{Maintainers: []string{"owner"}}},
			&Project{Name: "isv:kubernetes:core"},
		)

		require.NoError(t, Validate(config, ValidateOptions{RequiredMaintainers: []string{"owner"}}))
	})

	t.Run("distant ancestor", func(t *testing.T) {
		t.Parallel()

		config := newConfig(
			&Project{Name: "isv:kubernetes", People: People{Maintainers: []string{"owner"}}},
			&Project{Name: "isv:kubernetes:core"},
			&Project{Name: "isv:kubernetes:core:stable"},
		)

		require.NoError(t, Validate(config, ValidateOptions{RequiredMaintainers: []string{"owner"}}))
	})

	t.Run("no ancestor at all", func(t *testing.T) {
		t.Parallel()

		config := newConfig(&Project{Name: "isv:kubernetes:core"})

		err := Validate(config, ValidateOptions{})
		require.ErrorContains(t, err, "has no maintainer")
	})

	t.Run("descendants never grant ownership", func(t *testing.T) {
		t.Parallel()

		config := newConfig(
			&Project{Name: "isv:kubernetes"},
			&Project{Name: "isv:kubernetes:core", People: People{Maintainers: []string{"owner"}}},
		)

		err := Validate(config, ValidateOptions{})
		require.ErrorContains(t, err, `project "isv:kubernetes" has no maintainer`)
	})
}

func TestValidateDuplicateProjects(t *testing.T) {
	t.Parallel()

	first := &Project{Name: "isv:kubernetes", People: People{Maintainers: []string{"owner"}}}
	first.sourcePath = "a.yaml"

	second := &Project{Name: "isv:kubernetes", People: People{Maintainers: []string{"owner"}}}
	second.sourcePath = "b.yaml"

	err := Validate(newConfig(first, second), ValidateOptions{})
	require.ErrorContains(t, err, `b.yaml: project "isv:kubernetes" is already declared in a.yaml`)
}

// TestValidateHierarchy pins the shape check on the declared tree: a gap is an
// undeclared project between two declared ones. Anything above the topmost
// declared project is outside the tree obscli knows about and is not checked,
// which is what keeps a scoped run on a single deep project usable.
func TestValidateHierarchy(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		names   []string
		message string
	}{
		"gap between two declared projects": {
			names:   []string{"isv:kubernetes", "isv:kubernetes:core:stable"},
			message: `project "isv:kubernetes:core" between them is not declared`,
		},
		"two gaps between two declared projects": {
			names:   []string{"isv:kubernetes", "isv:kubernetes:core:stable:v1.38"},
			message: `project "isv:kubernetes:core" between them is not declared`,
		},
		"complete chain": {
			names: []string{
				"isv:kubernetes",
				"isv:kubernetes:core",
				"isv:kubernetes:core:stable",
				"isv:kubernetes:core:stable:v1.38",
			},
		},
		"single deep project is a tree of its own": {
			names: []string{"isv:kubernetes:core:stable:v1.38"},
		},
		"siblings without a declared parent": {
			names: []string{"isv:kubernetes:core:stable:v1.38", "isv:kubernetes:core:stable:v1.39"},
		},
		"unrelated trees": {
			names: []string{"isv:kubernetes:core", "home:someone:core"},
		},
		"top level name is never an ancestor to check": {
			names: []string{"home:someone", "home:someone:core"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			projects := make([]*Project, 0, len(testCase.names))
			for _, projectName := range testCase.names {
				projects = append(projects, &Project{
					Name:   projectName,
					People: People{Maintainers: []string{"owner"}},
				})
			}

			err := Validate(newConfig(projects...), ValidateOptions{})

			if testCase.message == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, testCase.message)
		})
	}
}

// TestValidateHierarchyReportsEveryGap pins that a chain with more than one
// missing link names all of them, so that one run fixes the whole config.
func TestValidateHierarchyReportsEveryGap(t *testing.T) {
	t.Parallel()

	err := Validate(newConfig(
		&Project{Name: "isv:kubernetes", People: People{Maintainers: []string{"owner"}}},
		&Project{
			Name:   "isv:kubernetes:core:stable:v1.38",
			People: People{Maintainers: []string{"owner"}},
		},
	), ValidateOptions{})

	require.ErrorContains(t, err, `project "isv:kubernetes:core:stable" between them is not declared`)
	require.ErrorContains(t, err, `project "isv:kubernetes:core" between them is not declared`)
}

func TestAncestors(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		[]string{"isv:kubernetes:core", "isv:kubernetes", "isv"},
		Ancestors("isv:kubernetes:core:stable"),
	)
	assert.Empty(t, Ancestors("isv"))
}

func TestValidateProjectFields(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		project *Project
		message string
	}{
		"missing name": {
			project: &Project{},
			message: "name is required",
		},
		"invalid name": {
			project: &Project{Name: "isv::kubernetes"},
			message: "is not a valid OBS project name",
		},
		"trailing colon": {
			project: &Project{Name: "isv:kubernetes:"},
			message: "is not a valid OBS project name",
		},
		// A bare top level name is a namespace whole unrelated trees live
		// under, so obscli must never own one: isv holds the Kubernetes tree,
		// home holds every user's projects.
		"top level name": {
			project: &Project{Name: "isv", People: People{Maintainers: []string{"owner"}}},
			message: "is a top level name, which obscli never manages",
		},
		"top level home namespace": {
			project: &Project{Name: "home", People: People{Maintainers: []string{"owner"}}},
			message: "is a top level name, which obscli never manages",
		},
		"invalid kind": {
			project: &Project{Name: "isv:kubernetes", Kind: "nope"},
			message: "invalid kind",
		},
		"invalid user": {
			project: &Project{Name: "isv:kubernetes", People: People{Maintainers: []string{"a/b"}}},
			message: "is not a valid OBS user name",
		},
		"duplicate user": {
			project: &Project{Name: "isv:kubernetes", People: People{Maintainers: []string{"a", "a"}}},
			message: `maintainer "a" is listed more than once`,
		},
		"invalid group": {
			project: &Project{
				Name:   "isv:kubernetes",
				People: People{Maintainers: []string{"owner"}},
				Groups: People{Bugowners: []string{"a:b"}},
			},
			message: "is not a valid OBS group name",
		},
		"repository without name": {
			project: &Project{
				Name:         "isv:kubernetes",
				People:       People{Maintainers: []string{"owner"}},
				Repositories: []Repository{{}},
			},
			message: "repository name is required",
		},
		"duplicate repository": {
			project: &Project{
				Name:         "isv:kubernetes",
				People:       People{Maintainers: []string{"owner"}},
				Repositories: []Repository{{Name: "rpm"}, {Name: "rpm"}},
			},
			message: `repository "rpm" is declared more than once`,
		},
		"duplicate architecture": {
			project: &Project{
				Name:         "isv:kubernetes",
				People:       People{Maintainers: []string{"owner"}},
				Repositories: []Repository{{Name: "rpm", Architectures: []string{"x86_64", "x86_64"}}},
			},
			message: `architecture "x86_64" is listed more than once`,
		},
		"path without repository": {
			project: &Project{
				Name:   "isv:kubernetes",
				People: People{Maintainers: []string{"owner"}},
				Repositories: []Repository{{
					Name:  "rpm",
					Paths: []RepositoryPath{{Project: "SUSE:SLE-15-SP5:GA"}},
				}},
			},
			message: "path requires a repository",
		},
		"release target without project": {
			project: &Project{
				Name:   "isv:kubernetes",
				People: People{Maintainers: []string{"owner"}},
				Repositories: []Repository{{
					Name:           "rpm",
					ReleaseTargets: []ReleaseTarget{{Repository: "rpm"}},
				}},
			},
			message: "release target requires a project",
		},
		"invalid trigger": {
			project: &Project{
				Name:   "isv:kubernetes",
				People: People{Maintainers: []string{"owner"}},
				Repositories: []Repository{{
					Name: "rpm",
					ReleaseTargets: []ReleaseTarget{
						{Project: "isv:target", Repository: "rpm", Trigger: "nope"},
					},
				}},
			},
			message: "invalid release target trigger",
		},
		"invalid package policy": {
			project: &Project{
				Name:     "isv:kubernetes",
				People:   People{Maintainers: []string{"owner"}},
				Packages: Packages{Policy: "nope"},
			},
			message: "invalid package policy",
		},
		"invalid subproject policy": {
			project: &Project{
				Name:        "isv:kubernetes",
				People:      People{Maintainers: []string{"owner"}},
				Subprojects: Subprojects{Policy: "nope"},
			},
			message: "invalid subproject policy",
		},
		"package without name": {
			project: &Project{
				Name:     "isv:kubernetes",
				People:   People{Maintainers: []string{"owner"}},
				Packages: Packages{Managed: []Package{{}}},
			},
			message: "package name is required",
		},
		"invalid package name": {
			project: &Project{
				Name:     "isv:kubernetes",
				People:   People{Maintainers: []string{"owner"}},
				Packages: Packages{Managed: []Package{{Name: "-kubelet"}}},
			},
			message: "is not a valid OBS package name",
		},
		"duplicate package": {
			project: &Project{
				Name:     "isv:kubernetes",
				People:   People{Maintainers: []string{"owner"}},
				Packages: Packages{Managed: []Package{{Name: "kubelet"}, {Name: "kubelet"}}},
			},
			message: `package "kubelet" is declared more than once`,
		},
		"managed release copy": {
			project: &Project{
				Name:     "isv:kubernetes",
				People:   People{Maintainers: []string{"owner"}},
				Packages: Packages{Managed: []Package{{Name: "kubelet.20260923195931"}}},
			},
			message: "is a release copy and must not be managed",
		},
		"invalid package maintainer": {
			project: &Project{
				Name:   "isv:kubernetes",
				People: People{Maintainers: []string{"owner"}},
				Packages: Packages{Managed: []Package{{
					Name:   "kubelet",
					People: People{Maintainers: []string{"a/b"}},
				}}},
			},
			message: "package kubelet: maintainer",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := Validate(newConfig(testCase.project), ValidateOptions{})
			require.ErrorContains(t, err, testCase.message)
		})
	}
}

func TestValidateRepositoryReferences(t *testing.T) {
	t.Parallel()

	const (
		sourceName = "isv:kubernetes:source"
		targetName = "isv:kubernetes:target"
	)

	for _, kind := range []string{"path", "release target"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			for name, testCase := range map[string]struct {
				reference          RepositoryPath
				targetRepositories []Repository
				declareTarget      bool
				message            string
			}{
				"declared repository exists": {
					reference:          RepositoryPath{Project: targetName, Repository: "rpm"},
					targetRepositories: []Repository{{Name: "rpm"}},
					declareTarget:      true,
				},
				"declared repository missing": {
					reference:          RepositoryPath{Project: targetName, Repository: "deb"},
					targetRepositories: []Repository{{Name: "rpm"}},
					declareTarget:      true,
					message:            `refers to repository "deb", which is not declared in project "` + targetName + `"`,
				},
				"declared project has no repositories": {
					reference:     RepositoryPath{Project: targetName, Repository: "rpm"},
					declareTarget: true,
					message:       `refers to repository "rpm", which is not declared in project "` + targetName + `"`,
				},
				"repository names are case sensitive": {
					reference:          RepositoryPath{Project: targetName, Repository: "RPM"},
					targetRepositories: []Repository{{Name: "rpm"}},
					declareTarget:      true,
					message:            `refers to repository "RPM", which is not declared in project "` + targetName + `"`,
				},
				"undeclared project remains external": {
					reference: RepositoryPath{Project: targetName, Repository: "rpm"},
				},
				"different repository in same project": {
					reference:          RepositoryPath{Project: sourceName, Repository: "base"},
					targetRepositories: []Repository{{Name: "base"}},
				},
				"missing repository in same project": {
					reference: RepositoryPath{Project: sourceName, Repository: "base"},
					message:   `refers to repository "base", which is not declared in project "` + sourceName + `"`,
				},
				"repository references itself": {
					reference: RepositoryPath{Project: sourceName, Repository: "rpm"},
					message:   "must not refer to its own repository " + sourceName + "/rpm",
				},
			} {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					repository := Repository{Name: "rpm"}
					if kind == "path" {
						repository.Paths = []RepositoryPath{testCase.reference}
					} else {
						repository.ReleaseTargets = []ReleaseTarget{{
							Project:    testCase.reference.Project,
							Repository: testCase.reference.Repository,
						}}
					}

					source := &Project{
						Name:         sourceName,
						People:       People{Maintainers: []string{"owner"}},
						Repositories: []Repository{repository},
					}

					projects := []*Project{source}
					if testCase.reference.Project == sourceName {
						source.Repositories = append(source.Repositories, testCase.targetRepositories...)
					} else if testCase.declareTarget {
						projects = append(projects, &Project{
							Name:         targetName,
							People:       People{Maintainers: []string{"owner"}},
							Repositories: testCase.targetRepositories,
						})
					}

					for _, reverse := range []bool{false, true} {
						if reverse && len(projects) == 2 {
							projects[0], projects[1] = projects[1], projects[0]
						}

						err := Validate(newConfig(projects...), ValidateOptions{})
						if testCase.message == "" {
							require.NoError(t, err)
						} else {
							require.EqualError(t, err,
								source.SourcePath()+": repository rpm: "+kind+" "+testCase.message)
						}
					}
				})
			}
		})
	}
}

func TestValidateReportsAllRepositoryReferenceErrors(t *testing.T) {
	t.Parallel()

	err := Validate(newConfig(
		&Project{
			Name:   "isv:kubernetes:source",
			People: People{Maintainers: []string{"owner"}},
			Repositories: []Repository{{
				Name: "rpm",
				Paths: []RepositoryPath{{
					Project: "isv:kubernetes:target", Repository: "missing-path",
				}},
				ReleaseTargets: []ReleaseTarget{{
					Project: "isv:kubernetes:target", Repository: "missing-release", Trigger: "invalid",
				}},
			}},
		},
		&Project{
			Name:   "isv:kubernetes:target",
			People: People{Maintainers: []string{"owner"}},
		},
	), ValidateOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `path refers to repository "missing-path"`)
	assert.Contains(t, err.Error(), `release target refers to repository "missing-release"`)
	assert.Contains(t, err.Error(), `invalid release target trigger "invalid"`)
}

func TestValidateRejectsMissingReleaseRepositoryInTestdata(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join("testdata", "valid"))
	require.NoError(t, err)

	for _, project := range cfg.Projects {
		if project.Name == "isv:kubernetes:core:stable:v1.34" {
			project.Repositories = []Repository{{Name: "rpm"}}
		}
	}

	err = Validate(cfg, ValidateOptions{})
	require.ErrorContains(t, err,
		`repository deb: release target refers to repository "deb", which is not declared in project "isv:kubernetes:core:stable:v1.34"`)
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()

	err := Validate(newConfig(&Project{
		Name:     "isv::kubernetes",
		Kind:     "nope",
		Packages: Packages{Policy: "nope"},
	}), ValidateOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid OBS project name")
	assert.Contains(t, err.Error(), "invalid kind")
	assert.Contains(t, err.Error(), "invalid package policy")
	assert.Contains(t, err.Error(), "has no maintainer")
}

func TestValidateEmptyConfig(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, Validate(nil, ValidateOptions{}), "declares no projects")
	require.ErrorContains(t, Validate(&Config{}, ValidateOptions{}), "declares no projects")
}

func TestValidateAcceptsAllKindsAndTriggers(t *testing.T) {
	t.Parallel()

	kinds := []obs.ProjectKind{
		"",
		obs.ProjectKindStandard,
		obs.ProjectKindMaintenance,
		obs.ProjectKindMaintenanceIncident,
		obs.ProjectKindMaintenanceRelease,
	}

	triggers := []obs.ReleaseTrigger{
		"",
		obs.ReleaseTriggerManual,
		obs.ReleaseTriggerMaintenance,
		obs.ReleaseTriggerObsGenDiff,
	}

	for _, kind := range kinds {
		for _, trigger := range triggers {
			project := &Project{
				Name:   "isv:kubernetes",
				Kind:   kind,
				People: People{Maintainers: []string{"owner"}},
				Repositories: []Repository{{
					Name: "rpm",
					ReleaseTargets: []ReleaseTarget{
						{Project: "isv:target", Repository: "rpm", Trigger: trigger},
					},
				}},
			}

			require.NoError(t, Validate(newConfig(project), ValidateOptions{}))
		}
	}
}

func TestNameValidation(t *testing.T) {
	t.Parallel()

	t.Run("project", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"isv", "isv:kubernetes:core:stable:v1.34:build", "a-b+c.d"} {
			assert.True(t, ValidProjectName(name), name)
		}

		for _, name := range []string{
			"", "0", ":isv", ".isv", "_isv", "isv:", "isv::core", "isv:.core",
			"isv:_core", "isv kubernetes", "isv/core",
		} {
			assert.False(t, ValidProjectName(name), name)
		}
	})

	t.Run("package", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{
			"kubelet", "cri-tools", "python3.13", "a+b",
			"_product", "_pattern", "_project", "_patchinfo",
			"_product:example", "_patchinfo:example",
		} {
			assert.True(t, ValidPackageName(name), name)
		}

		for _, name := range []string{
			"", "0", "-kubelet", ".kubelet", "_kubelet", "kube let", "a:b",
			"_product:", "_patchinfo:", "_product:../other",
		} {
			assert.False(t, ValidPackageName(name), name)
		}
	})

	t.Run("user", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"xmudrii", "k8s-release-bot", "a.b", "a_b"} {
			assert.True(t, ValidUserName(name), name)
		}

		for _, name := range []string{"", "a", "a/b", "a:b"} {
			assert.False(t, ValidUserName(name), name)
		}
	})

	t.Run("group", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"k8s-release-team", "a.b", "a_b"} {
			assert.True(t, ValidGroupName(name), name)
		}

		for _, name := range []string{"", "a:b", "a b", "a/b"} {
			assert.False(t, ValidGroupName(name), name)
		}
	})
}

// newConfig builds a config from projects, giving each a source path so that
// error messages stay readable.
func newConfig(projects ...*Project) *Config {
	for i, project := range projects {
		if project.sourcePath == "" {
			project.sourcePath = filepath.Join("testdata", "generated", string(rune('a'+i))+".yaml")
		}
	}

	return &Config{Projects: projects}
}
