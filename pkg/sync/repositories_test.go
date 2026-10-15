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
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/release-sdk/obs"

	"sigs.k8s.io/obscli/pkg/config"
)

func TestReconcileStagesRepositoryReplacement(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"paths", "releaseTargets"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			cfg, dryClient := repositoryTransition(t, field)
			before := NormalizeProject(dryClient.projects[cfg.Projects[1].Name])
			drySummary, dryOutput := reconcile(t, dryClient, cfg, Options{})
			assert.Empty(t, dryClient.mutatingCalls())
			assert.Equal(t, before, NormalizeProject(dryClient.projects[before.Name]))

			cfg, client := repositoryTransition(t, field)
			consumer, target := cfg.Projects[0], cfg.Projects[1]
			prepared := NormalizeProject(client.projects[target.Name])
			prepared.Repositories = append(prepared.Repositories, obs.Repository{
				Name: "new", Architectures: []string{"x86_64", "aarch64"},
			})
			retained := ProjectMeta(target)
			old := client.projects[target.Name].Repositories[0]
			retained.Repositories = append(retained.Repositories, old)

			summary, output := reconcile(t, client, cfg, Options{Confirm: true})
			assert.Equal(t, Summary{ProjectsUpdated: 2}, summary)
			assert.Equal(t, drySummary, summary)
			assert.Equal(t, dryOutput, output)
			assert.Equal(t, []*obs.Project{
				prepared, retained, ProjectMeta(consumer), ProjectMeta(target),
			}, client.projectWrites)

			for _, project := range cfg.Projects {
				assert.Equal(t, ProjectMeta(project), NormalizeProject(client.projects[project.Name]))
				assert.Equal(t, 1, strings.Count(output, "project "+project.Name+"\n"))
			}

			assert.NotContains(t, client.reads, "external:base")

			client.calls, client.projectWrites = nil, nil
			summary, output = reconcile(t, client, cfg, Options{Confirm: true})
			assert.Equal(t, Summary{}, summary)
			assert.Empty(t, output)
			assert.Empty(t, client.mutatingCalls())
			assert.Empty(t, client.projectWrites)
		})
	}
}

func TestReconcilePreparesSameProjectReleaseTargets(t *testing.T) {
	t.Parallel()

	for name, existing := range map[string]bool{"new project": false, "existing project": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := buildConfig(t, `name: isv:kubernetes
title: New
maintainers: [owner]
repositories:
  - name: build
    architectures: [aarch64, x86_64]
    paths:
      - project: isv:kubernetes
        repository: release
    releaseTargets:
      - project: isv:kubernetes
        repository: release
  - name: release
    architectures: [x86_64]
`)
			require.NoError(t, config.Validate(cfg, config.ValidateOptions{}))

			newClient := func() *fakeClient {
				client := newFakeClient()
				if existing {
					client.addProject(&obs.Project{
						Name: "isv:kubernetes", Title: "Old",
						Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
					})
				}

				return client
			}
			dryClient := newClient()
			drySummary, dryOutput := reconcile(t, dryClient, cfg, Options{})
			assert.Empty(t, dryClient.mutatingCalls())

			client := newClient()

			prepared := MinimalProjectMeta("isv:kubernetes")
			if existing {
				prepared = NormalizeProject(client.projects["isv:kubernetes"])
			}

			prepared.Repositories = []obs.Repository{
				{Name: "build", Architectures: []string{"aarch64", "x86_64"}},
				{Name: "release", Architectures: []string{"x86_64"}},
			}

			expected := []*obs.Project{prepared, ProjectMeta(cfg.Projects[0])}
			if !existing {
				expected = append([]*obs.Project{MinimalProjectMeta("isv:kubernetes")}, expected...)
			}

			summary, output := reconcile(t, client, cfg, Options{Confirm: true})
			assert.Equal(t, expected, client.projectWrites)
			assert.Equal(t, drySummary, summary)
			assert.Equal(t, dryOutput, output)

			if existing {
				assert.Equal(t, Summary{ProjectsUpdated: 1}, summary)
			} else {
				assert.Equal(t, Summary{ProjectsCreated: 1}, summary)
			}

			assert.Equal(t, 1, strings.Count(output, "project isv:kubernetes\n"))
			assert.Equal(t, ProjectMeta(cfg.Projects[0]), NormalizeProject(client.projects["isv:kubernetes"]))
		})
	}
}

func TestReconcileDetachesObsoleteRepositoryReferences(t *testing.T) {
	t.Parallel()

	t.Run("within a project", func(t *testing.T) {
		t.Parallel()
		cfg := buildConfig(t, "name: isv:kubernetes\nmaintainers: [owner]\n")
		client := newFakeClient()
		live := ProjectMeta(cfg.Projects[0])
		live.Repositories = []obs.Repository{
			{Name: "old-target"},
			{
				Name:           "old-consumer",
				Paths:          []obs.RepositoryPath{{Project: live.Name, Repository: "old-target"}},
				ReleaseTargets: []obs.ReleaseTarget{{Project: live.Name, Repository: "old-target"}},
			},
		}
		client.addProject(live)

		summary, _ := reconcile(t, client, cfg, Options{Confirm: true})
		assert.Equal(t, Summary{ProjectsUpdated: 1}, summary)
		require.Len(t, client.projectWrites, 2)
		assert.Equal(t, []obs.Repository{{Name: "old-target"}, {Name: "old-consumer"}},
			client.projectWrites[0].Repositories)
		assert.Empty(t, client.projectWrites[1].Repositories)
	})

	t.Run("between projects", func(t *testing.T) {
		t.Parallel()
		cfg := buildConfig(t,
			"name: isv:kubernetes:a\nmaintainers: [owner]\n",
			"name: isv:kubernetes:b\nmaintainers: [owner]\n",
		)
		client := newFakeClient()

		for i, project := range cfg.Projects {
			live := ProjectMeta(project)
			live.Repositories = []obs.Repository{{
				Name: "old",
				Paths: []obs.RepositoryPath{{
					Project: cfg.Projects[1-i].Name, Repository: "old",
				}},
			}}
			client.addProject(live)
		}

		summary, _ := reconcile(t, client, cfg, Options{Confirm: true})
		assert.Equal(t, Summary{ProjectsUpdated: 2}, summary)
		require.Len(t, client.projectWrites, 4)

		for _, write := range client.projectWrites[:2] {
			assert.Equal(t, []obs.Repository{{Name: "old"}}, write.Repositories)
		}

		for _, write := range client.projectWrites[2:] {
			assert.Empty(t, write.Repositories)
		}
	})
}

func TestReconcileStopsRepositoryStagesOnFailure(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		write   int
		updated int
	}{
		"preparation":       {write: 1, updated: 0},
		"target metadata":   {write: 2, updated: 1},
		"consumer metadata": {write: 3, updated: 1},
		"cleanup":           {write: 4, updated: 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg, client := repositoryTransition(t, "paths")
			target := cfg.Projects[1]
			target.Packages.Policy = config.PolicyPrune
			client.addPackages(target.Name, "leftover")

			writeErr := errors.New("injected failure")
			client.projectWriteErr = func(*obs.Project) error {
				if len(client.projectWrites) == test.write {
					return writeErr
				}

				return nil
			}

			summary, err := Reconcile(t.Context(), client, cfg, NewLogger(io.Discard), Options{
				Confirm: true, AllowDeletions: true,
			})
			require.ErrorIs(t, err, writeErr)
			assert.Equal(t, Summary{ProjectsUpdated: test.updated}, summary)
			assert.Len(t, client.projectWrites, test.write)
			assert.True(t, hasRepository(client.projects[target.Name].Repositories, "old"))
			assert.Contains(t, client.packages[target.Name], "leftover")

			for _, call := range client.mutatingCalls() {
				assert.NotContains(t, call, "delete ")
			}

			if test.write == 1 {
				assert.False(t, hasRepository(client.projects[target.Name].Repositories, "new"))
			} else {
				assert.True(t, hasRepository(client.projects[target.Name].Repositories, "new"))
			}
		})
	}
}

func TestReconcileRetainsRepositoriesWhenPackageWriteFails(t *testing.T) {
	t.Parallel()
	cfg, client := repositoryTransition(t, "paths")
	cfg.Projects[0].Packages.Managed = []config.Package{{Name: "kubelet"}}
	cfg.Projects[1].Packages.Policy = config.PolicyPrune
	old := client.projects[cfg.Projects[1].Name].Repositories[0]
	client.addPackages(cfg.Projects[1].Name, "leftover")
	client.packageWriteErr = errors.New("package write failed")

	summary, err := Reconcile(t.Context(), client, cfg, NewLogger(io.Discard), Options{
		Confirm: true, AllowDeletions: true,
	})
	require.ErrorIs(t, err, client.packageWriteErr)
	assert.Equal(t, Summary{ProjectsUpdated: 2}, summary)
	assert.Len(t, client.projectWrites, 3)
	require.Len(t, client.projects[cfg.Projects[1].Name].Repositories, 2)
	assert.Equal(t, old, client.projects[cfg.Projects[1].Name].Repositories[1])
	assert.Contains(t, client.packages[cfg.Projects[1].Name], "leftover")
}

func TestReconcileDoesNotDetachObsoleteReferencesBeforePackageUpdates(t *testing.T) {
	t.Parallel()
	cfg := buildConfig(t, "name: isv:kubernetes\nmaintainers: [owner]\npackages:\n  managed:\n    - name: kubelet\n")
	client := newFakeClient()
	live := ProjectMeta(cfg.Projects[0])
	live.Repositories = []obs.Repository{
		{Name: "old-target"},
		{
			Name:           "old-consumer",
			Paths:          []obs.RepositoryPath{{Project: live.Name, Repository: "old-target"}},
			ReleaseTargets: []obs.ReleaseTarget{{Project: live.Name, Repository: "old-target"}},
		},
	}
	client.addProject(live)
	client.packageWriteErr = errors.New("package write failed")

	summary, err := Reconcile(t.Context(), client, cfg, NewLogger(io.Discard), Options{Confirm: true})
	require.ErrorIs(t, err, client.packageWriteErr)
	assert.Equal(t, Summary{}, summary)
	assert.Empty(t, client.projectWrites)
	assert.Equal(t, live, client.projects[live.Name])
}

func TestReconcileStopsWhenDetachingObsoleteReferencesFails(t *testing.T) {
	t.Parallel()
	cfg := buildConfig(t, "name: isv:kubernetes\nmaintainers: [owner]\npackages: {policy: prune}\n")
	client := newFakeClient()
	live := ProjectMeta(cfg.Projects[0])
	live.Repositories = []obs.Repository{
		{Name: "old-target"},
		{
			Name:  "old-consumer",
			Paths: []obs.RepositoryPath{{Project: live.Name, Repository: "old-target"}},
		},
	}
	client.addProject(live)
	client.addPackages(live.Name, "leftover")
	client.mutationErr = errors.New("detaching references failed")

	summary, err := Reconcile(t.Context(), client, cfg, NewLogger(io.Discard), Options{
		Confirm: true, AllowDeletions: true,
	})
	require.ErrorIs(t, err, client.mutationErr)
	require.ErrorContains(t, err, "detaching obsolete repository references")
	assert.Equal(t, Summary{}, summary)
	assert.Equal(t, []string{"put project " + live.Name}, client.mutatingCalls())
	assert.Equal(t, live, client.projects[live.Name])
	assert.Contains(t, client.packages[live.Name], "leftover")
}

func TestReconcileRepositoryStagesRequireSuccessfulLogging(t *testing.T) {
	t.Parallel()

	for _, stage := range []string{"preparation", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			cfg := buildConfig(t, "name: isv:kubernetes\nmaintainers: [owner]\n")
			client := newFakeClient()
			live := ProjectMeta(cfg.Projects[0])
			live.Repositories = []obs.Repository{{Name: "old"}}
			client.addProject(live)

			if stage == "preparation" {
				cfg.Projects[0].Repositories = []config.Repository{{Name: "new"}}
			}

			summary, err := Reconcile(t.Context(), client, cfg, NewLogger(failingWriter{}), Options{Confirm: true})
			require.ErrorContains(t, err, "writing log output")
			assert.Equal(t, Summary{}, summary)
			assert.Empty(t, client.mutatingCalls())
			assert.Equal(t, live, client.projects[live.Name])
		})
	}
}

func TestReconcileDoesNotForceRepositoryRemoval(t *testing.T) {
	t.Parallel()
	cfg, client := repositoryTransition(t, "paths")
	consumer, target := cfg.Projects[0], cfg.Projects[1]
	cfg.Projects = []*config.Project{target}
	originalConsumer := NormalizeProject(client.projects[consumer.Name])
	old := client.projects[target.Name].Repositories[0]

	summary, err := Reconcile(t.Context(), client, cfg, NewLogger(io.Discard), Options{Confirm: true})
	require.ErrorContains(t, err, "removing obsolete repositories")
	require.ErrorContains(t, err, "still referenced by "+consumer.Name)
	assert.Equal(t, Summary{ProjectsUpdated: 1}, summary)
	assert.Equal(t, []string{target.Name}, client.reads)
	assert.Equal(t, originalConsumer, NormalizeProject(client.projects[consumer.Name]))
	require.Len(t, client.projects[target.Name].Repositories, 2)
	assert.Equal(t, old, client.projects[target.Name].Repositories[1])
}

func TestFakeClientModelsRepositoryConstraints(t *testing.T) {
	t.Parallel()

	t.Run("forward paths are resolved in a second pass", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(MinimalProjectMeta("isv:kubernetes"))
		require.NoError(t, client.PutProjectMeta(t.Context(), &obs.Project{
			Name: "isv:kubernetes",
			Repositories: []obs.Repository{
				{Name: "build", Paths: []obs.RepositoryPath{{Project: "isv:kubernetes", Repository: "release"}}},
				{Name: "release"},
			},
		}))
	})

	t.Run("release targets must exist during the first pass", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(MinimalProjectMeta("isv:kubernetes"))

		meta := &obs.Project{
			Name: "isv:kubernetes",
			Repositories: []obs.Repository{
				{Name: "build", ReleaseTargets: []obs.ReleaseTarget{{Project: "isv:kubernetes", Repository: "release"}}},
				{Name: "release"},
			},
		}
		require.ErrorContains(t, client.PutProjectMeta(t.Context(), meta), "repository isv:kubernetes/release does not exist")
		assert.Empty(t, client.projects[meta.Name].Repositories)
		meta.Repositories[0], meta.Repositories[1] = meta.Repositories[1], meta.Repositories[0]
		require.NoError(t, client.PutProjectMeta(t.Context(), meta))
	})

	for _, field := range []string{"paths", "releaseTargets"} {
		t.Run("incoming "+field+" block deletion", func(t *testing.T) {
			t.Parallel()
			cfg, client := repositoryTransition(t, field)
			target := cfg.Projects[1]
			before := NormalizeProject(client.projects[target.Name])
			require.ErrorContains(t, client.PutProjectMeta(t.Context(), ProjectMeta(target)), "still")
			assert.Equal(t, before, NormalizeProject(client.projects[target.Name]))
		})
	}
}

func repositoryTransition(t *testing.T, field string) (*config.Config, *fakeClient) {
	t.Helper()
	cfg := buildConfig(t,
		"name: isv:kubernetes:consumer\nmaintainers: [owner]\nsubprojects: {policy: ignore}\n"+
			"repositories:\n  - name: rpm\n    "+field+":\n"+
			"      - project: isv:kubernetes:target\n        repository: new\n",
		"name: isv:kubernetes:target\ntitle: New target\nmaintainers: [owner]\nsubprojects: {policy: ignore}\n"+
			"repositories:\n  - name: new\n    architectures: [x86_64, aarch64]\n",
	)
	require.NoError(t, config.Validate(cfg, config.ValidateOptions{}))

	consumer := ProjectMeta(cfg.Projects[0])
	if field == "paths" {
		consumer.Repositories[0].Paths[0].Repository = "old"
	} else {
		consumer.Repositories[0].ReleaseTargets[0].Repository = "old"
	}

	target := ProjectMeta(cfg.Projects[1])
	target.Title = "Old target"
	target.Description = "Preserved during preparation"
	target.URL = "https://example.invalid"
	target.Persons = append(target.Persons, obs.Person{UserID: "another-owner", Role: obs.RoleMaintainer})
	target.Build = obs.Flag{{Action: "disable"}}
	target.Repositories = []obs.Repository{{
		Name: "old", Rebuild: "local", Block: "all", LinkedBuild: "off", Architectures: []string{"x86_64"},
		Paths:          []obs.RepositoryPath{{Project: "external:base", Repository: "standard"}},
		ReleaseTargets: []obs.ReleaseTarget{{Project: "external:base", Repository: "standard"}},
	}}
	client := newFakeClient()
	client.addProject(consumer)
	client.addProject(target)
	client.addProject(&obs.Project{
		Name: "external:base", Repositories: []obs.Repository{{Name: "standard"}},
	})

	return cfg, client
}

func TestReconcileRepositoryRemovalFollowsPackageUpdates(t *testing.T) {
	t.Parallel()
	cfg, client := repositoryTransition(t, "paths")
	consumer, target := cfg.Projects[0], cfg.Projects[1]
	consumer.Packages.Managed = []config.Package{{Name: "kubelet"}}
	target.Packages.Policy = config.PolicyPrune
	client.addPackages(target.Name, "leftover")

	var output bytes.Buffer

	summary, err := Reconcile(t.Context(), client, cfg, NewLogger(&output), Options{
		Confirm: true, AllowDeletions: true,
	})
	require.NoError(t, err)
	assert.Equal(t, Summary{ProjectsUpdated: 2, PackagesCreated: 1, PackagesDeleted: 1}, summary)
	assert.Equal(t, []string{
		"put project " + target.Name,
		"put project " + target.Name,
		"put project " + consumer.Name,
		"put package " + consumer.Name + "/kubelet",
		"put project " + target.Name,
		"delete package " + target.Name + "/leftover",
	}, client.mutatingCalls())
}
