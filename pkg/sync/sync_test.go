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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/release-sdk/obs"

	"sigs.k8s.io/obscli/pkg/config"
)

// TestLiveStateProducesNoChanges is the most important guarantee of the whole
// tool: a configuration describing the live state must not produce a single
// write. The live states are meta documents captured verbatim from the public
// OBS API, so that the test fails if normalization stops covering what OBS
// actually returns.
func TestLiveStateProducesNoChanges(t *testing.T) {
	t.Parallel()

	for _, fixture := range []string{"project_builder", "project_release"} {
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()

			live := loadFixtureProject(t, fixture+".xml")
			desired := loadFixtureConfig(t, fixture+".yaml")

			require.Empty(t, ProjectChanges(ProjectMeta(desired), live))
			assert.True(t, ProjectsEqual(ProjectMeta(desired), live))
		})
	}
}

func TestReconcileRejectsInvalidProjectMetadataBeforeMutations(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		meta    *obs.Project
		wantErr string
	}{
		"missing metadata": {
			wantErr: "OBS returned no project metadata",
		},
		"missing name": {
			meta:    &obs.Project{},
			wantErr: `OBS returned metadata for ""`,
		},
		"unrelated project": {
			meta:    &obs.Project{Name: "home:unrelated"},
			wantErr: `OBS returned metadata for "home:unrelated"`,
		},
		"another declared project": {
			meta:    &obs.Project{Name: "isv:kubernetes:a"},
			wantErr: `OBS returned metadata for "isv:kubernetes:a"`,
		},
	} {
		for mode, confirm := range map[string]bool{"dry run": false, "confirmed": true} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				cfg := buildConfig(t,
					"name: isv:kubernetes:a\nmaintainers: [owner]\n",
					"name: isv:kubernetes:b\nmaintainers: [owner]\nrepositories:\n  - name: new\n",
				)
				require.NoError(t, config.Validate(cfg, config.ValidateOptions{}))

				client := newFakeClient()
				client.projects["isv:kubernetes:b"] = test.meta

				var output bytes.Buffer

				summary, err := Reconcile(t.Context(), client, cfg, NewLogger(&output), Options{
					Confirm: confirm, AllowDeletions: true,
				})
				require.ErrorContains(t, err, test.wantErr)
				assert.Contains(t, err.Error(), "loading the live state of project isv:kubernetes:b")
				assert.Equal(t, Summary{}, summary)
				assert.Empty(t, output.String())
				assert.Equal(t, []string{"isv:kubernetes:a", "isv:kubernetes:b"}, client.reads)
				assert.Empty(t, client.mutatingCalls())
				assert.NotContains(t, client.projects, "isv:kubernetes:a")
			})
		}
	}
}

func TestReconcileCreatesProjectsParentsFirst(t *testing.T) {
	t.Parallel()

	client := newFakeClient()

	cfg := buildConfig(t,
		"name: isv:kubernetes:core:stable\ntitle: Stable\nmaintainers: [owner]\n",
		"name: isv:kubernetes\ntitle: Kubernetes\nmaintainers: [owner]\n",
		"name: isv:kubernetes:core\ntitle: Core\nmaintainers: [owner]\n",
	)

	summary, output := reconcile(t, client, cfg, Options{Confirm: true})

	require.Equal(t, 3, summary.ProjectsCreated)
	assert.Equal(t, 0, summary.ProjectsUpdated)

	// Creation remains parent-first. Without dependencies, metadata updates
	// retain the same hierarchy order.
	assert.Equal(t, []string{
		"put project isv:kubernetes",
		"put project isv:kubernetes:core",
		"put project isv:kubernetes:core:stable",
		"put project isv:kubernetes",
		"put project isv:kubernetes:core",
		"put project isv:kubernetes:core:stable",
	}, client.mutatingCalls())

	assert.Equal(t, "Kubernetes", client.projects["isv:kubernetes"].Title)

	// The log is grouped per project rather than split across the two phases.
	assert.Equal(t, []string{
		"+ project isv:kubernetes",
		`    title: "" → "Kubernetes"`,
		"    maintainers: +owner",
		"+ project isv:kubernetes:core",
		`    title: "" → "Core"`,
		"    maintainers: +owner",
		"+ project isv:kubernetes:core:stable",
		`    title: "" → "Stable"`,
		"    maintainers: +owner",
	}, lines(output))
}

func TestReconcileAppliesMetadataInDependencyOrder(t *testing.T) {
	t.Parallel()

	for name, existing := range map[string]bool{
		"new projects":      false,
		"existing projects": true,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := buildConfig(t,
				"name: isv:kubernetes:a\ntitle: A\nrepositories:\n"+
					"  - name: rpm\n    paths:\n"+
					"      - project: isv:kubernetes:b\n        repository: rpm\n"+
					"  - name: deb\n    paths:\n"+
					"      - project: isv:kubernetes:b\n        repository: rpm\n",
				"name: isv:kubernetes:c\ntitle: C\nrepositories:\n  - name: rpm\n",
				"name: isv:kubernetes\ntitle: Root\nmaintainers: [owner]\n",
				"name: isv:kubernetes:b\ntitle: B\nrepositories:\n"+
					"  - name: rpm\n    releaseTargets:\n"+
					"      - project: isv:kubernetes:c\n        repository: rpm\n",
			)
			require.NoError(t, config.Validate(cfg, config.ValidateOptions{}))

			newClient := func() *fakeClient {
				client := newFakeClient()

				if existing {
					for _, project := range cfg.Projects {
						client.addProject(MinimalProjectMeta(project.Name))
					}
				}

				return client
			}

			dryClient := newClient()
			drySummary, dryOutput := reconcile(t, dryClient, cfg, Options{})
			assert.Empty(t, dryClient.mutatingCalls())

			client := newClient()
			summary, output := reconcile(t, client, cfg, Options{Confirm: true})
			assert.Equal(t, drySummary, summary)
			assert.Equal(t, dryOutput, output)

			expected := []string{
				"put project isv:kubernetes:a",
				"put project isv:kubernetes:b",
				"put project isv:kubernetes:c",
				"put project isv:kubernetes",
				"put project isv:kubernetes:c",
				"put project isv:kubernetes:b",
				"put project isv:kubernetes:a",
			}

			if existing {
				assert.Equal(t, 0, summary.ProjectsCreated)
				assert.Equal(t, 4, summary.ProjectsUpdated)
			} else {
				assert.Equal(t, 4, summary.ProjectsCreated)
				assert.Equal(t, 0, summary.ProjectsUpdated)

				expected = append([]string{
					"put project isv:kubernetes",
					"put project isv:kubernetes:a",
					"put project isv:kubernetes:b",
					"put project isv:kubernetes:c",
				}, expected...)
			}

			assert.Equal(t, expected, client.mutatingCalls())

			for _, project := range cfg.Projects {
				assert.True(t, ProjectsEqual(ProjectMeta(project), client.projects[project.Name]), project.Name)
			}

			assert.Equal(t, "isv:kubernetes:a", cfg.Projects[0].Name, "config order must not change")

			client.calls = nil
			summary, output = reconcile(t, client, cfg, Options{Confirm: true})
			assert.False(t, summary.Changed())
			assert.Empty(t, output)
			assert.Empty(t, client.mutatingCalls())
		})
	}
}

func TestReconcileDependencyOrderIgnoresLocalAndExternalReferences(t *testing.T) {
	t.Parallel()

	client := newFakeClient()
	client.addProject(&obs.Project{
		Name: "external:base", Repositories: []obs.Repository{{Name: "standard"}},
	})

	cfg := buildConfig(t,
		"name: isv:kubernetes\nmaintainers: [owner]\nrepositories:\n"+
			"  - name: rpm\n    paths:\n"+
			"      - project: isv:kubernetes\n        repository: base\n"+
			"      - project: external:base\n        repository: standard\n"+
			"    releaseTargets:\n"+
			"      - project: external:base\n        repository: standard\n"+
			"  - name: base\n",
	)
	require.NoError(t, config.Validate(cfg, config.ValidateOptions{}))

	summary, _ := reconcile(t, client, cfg, Options{Confirm: true})
	assert.Equal(t, 1, summary.ProjectsCreated)
	assert.Equal(t, []string{"isv:kubernetes"}, client.reads)
	assert.Equal(t, []string{
		"put project isv:kubernetes",
		"put project isv:kubernetes",
		"put project isv:kubernetes",
	}, client.mutatingCalls())
}

func TestReconcileCreatesParentsBeforeDependencyOrderedMetadata(t *testing.T) {
	t.Parallel()

	client := newFakeClient()
	cfg := buildConfig(t,
		"name: isv:kubernetes:build\nrepositories:\n  - name: rpm\n",
		"name: isv:kubernetes\nmaintainers: [owner]\nrepositories:\n"+
			"  - name: rpm\n    paths:\n"+
			"      - project: isv:kubernetes:build\n        repository: rpm\n",
	)
	require.NoError(t, config.Validate(cfg, config.ValidateOptions{}))

	summary, _ := reconcile(t, client, cfg, Options{Confirm: true})
	assert.Equal(t, 2, summary.ProjectsCreated)
	assert.Equal(t, 0, summary.ProjectsUpdated)
	assert.Equal(t, []string{
		"put project isv:kubernetes",
		"put project isv:kubernetes:build",
		"put project isv:kubernetes",
		"put project isv:kubernetes:build",
		"put project isv:kubernetes",
	}, client.mutatingCalls())
}

func TestReconcileRejectsDependencyCyclesBeforeOBSRequests(t *testing.T) {
	t.Parallel()

	for name, fields := range map[string][2]string{
		"repository paths": {"paths", "paths"},
		"release targets":  {"releaseTargets", "releaseTargets"},
		"mixed references": {"paths", "releaseTargets"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := buildConfig(t,
				"name: isv:kubernetes\ntitle: Root\nmaintainers: [owner]\n",
				"name: isv:kubernetes:b\nrepositories:\n  - name: rpm\n    "+fields[1]+":\n"+
					"      - project: isv:kubernetes:a\n        repository: rpm\n",
				"name: isv:kubernetes:a\nrepositories:\n  - name: rpm\n    "+fields[0]+":\n"+
					"      - project: isv:kubernetes:b\n        repository: rpm\n",
			)
			require.NoError(t, config.Validate(cfg, config.ValidateOptions{}))

			for _, confirm := range []bool{false, true} {
				client := newFakeClient()
				output := &bytes.Buffer{}
				summary, err := Reconcile(t.Context(), client, cfg, NewLogger(output), Options{
					Confirm: confirm, AllowDeletions: true,
				})

				require.EqualError(t, err,
					"project metadata dependency cycle: isv:kubernetes:a -> isv:kubernetes:b -> isv:kubernetes:a")
				assert.Equal(t, Summary{}, summary)
				assert.Empty(t, client.reads)
				assert.Empty(t, client.calls)
				assert.Empty(t, output.String())
			}
		})
	}
}

func TestReconcileDryRunWritesNothing(t *testing.T) {
	t.Parallel()

	client := newFakeClient()

	cfg := buildConfig(t, "name: isv:kubernetes\ntitle: Kubernetes\nmaintainers: [owner]\n")

	summary, output := reconcile(t, client, cfg, Options{})

	assert.Equal(t, 1, summary.ProjectsCreated)
	assert.NotContains(t, client.projects, "isv:kubernetes")
	assert.Empty(t, client.mutatingCalls())
	assert.Contains(t, output, "+ project isv:kubernetes")
}

func TestReconcileCountsOnlySuccessfulMutations(t *testing.T) {
	t.Parallel()

	t.Run("project creation", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.mutationErr = errors.New("write failed")

		cfg := buildConfig(t, "name: isv:kubernetes\nmaintainers: [owner]\n")
		summary, err := Reconcile(t.Context(), client, cfg, NewLogger(&bytes.Buffer{}), Options{Confirm: true})

		require.ErrorContains(t, err, "write failed")
		assert.Equal(t, 0, summary.ProjectsCreated)
	})

	t.Run("project update", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(&obs.Project{
			Name:    "isv:kubernetes",
			Title:   "Old",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})
		client.mutationErr = errors.New("write failed")

		cfg := buildConfig(t, "name: isv:kubernetes\ntitle: New\nmaintainers: [owner]\n")
		summary, err := Reconcile(t.Context(), client, cfg, NewLogger(&bytes.Buffer{}), Options{Confirm: true})

		require.ErrorContains(t, err, "write failed")
		assert.Equal(t, 0, summary.ProjectsUpdated)
	})

	t.Run("package deletion", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(&obs.Project{
			Name:    "isv:kubernetes",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})
		client.addPackages("isv:kubernetes", "leftover")
		client.mutationErr = errors.New("delete failed")

		cfg := buildConfig(t, "name: isv:kubernetes\nmaintainers: [owner]\n"+
			"packages:\n  policy: prune\n")
		summary, err := Reconcile(t.Context(), client, cfg, NewLogger(&bytes.Buffer{}), Options{
			Confirm:        true,
			AllowDeletions: true,
		})

		require.ErrorContains(t, err, "delete failed")
		assert.Equal(t, 0, summary.PackagesDeleted)
	})
}

func TestReconcileStopsBeforeMutationWhenLoggingFails(t *testing.T) {
	t.Parallel()

	t.Run("missing project", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		cfg := buildConfig(t,
			"name: isv:kubernetes\nmaintainers: [owner]\n",
			"name: isv:kubernetes:core\nmaintainers: [owner]\n",
		)

		summary, err := Reconcile(
			t.Context(), client, cfg, NewLogger(failingWriter{}), Options{Confirm: true},
		)

		require.ErrorContains(t, err, "writing log output")
		assert.Empty(t, client.mutatingCalls())
		assert.Equal(t, 0, summary.ProjectsCreated)
	})

	t.Run("existing project", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(&obs.Project{
			Name:    "isv:kubernetes",
			Title:   "Old",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})

		cfg := buildConfig(t, "name: isv:kubernetes\ntitle: New\nmaintainers: [owner]\n")
		_, err := Reconcile(t.Context(), client, cfg, NewLogger(failingWriter{}), Options{Confirm: true})

		require.ErrorContains(t, err, "writing log output")
		assert.Empty(t, client.mutatingCalls())
		assert.Equal(t, "Old", client.projects["isv:kubernetes"].Title)
	})

	t.Run("package deletion", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		cfg := buildConfig(t,
			"name: isv:kubernetes\nmaintainers: [owner]\npackages:\n  policy: prune\n")
		client.addProject(ProjectMeta(cfg.Projects[0]))
		client.addPackages("isv:kubernetes", "leftover")

		summary, err := Reconcile(t.Context(), client, cfg, NewLogger(failingWriter{}), Options{
			Confirm: true, AllowDeletions: true,
		})

		require.ErrorContains(t, err, "writing log output")
		assert.Empty(t, client.mutatingCalls())
		assert.Zero(t, summary.PackagesDeleted)
		assert.Contains(t, client.packages["isv:kubernetes"], "leftover")
	})
}

func TestReconcileUpdatesOnlyWhatChanged(t *testing.T) {
	t.Parallel()

	newClient := func() *fakeClient {
		client := newFakeClient()
		client.addProject(&obs.Project{
			Name:    "isv:kubernetes",
			Title:   "Kubernetes",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})

		return client
	}

	t.Run("no change", func(t *testing.T) {
		t.Parallel()

		client := newClient()
		cfg := buildConfig(t, "name: isv:kubernetes\ntitle: Kubernetes\nmaintainers: [owner]\n")

		summary, output := reconcile(t, client, cfg, Options{Confirm: true})

		assert.False(t, summary.Changed())
		assert.Empty(t, client.mutatingCalls())
		assert.Empty(t, output)
	})

	t.Run("changed title and people", func(t *testing.T) {
		t.Parallel()

		client := newClient()
		cfg := buildConfig(t,
			"name: isv:kubernetes\ntitle: Kubernetes Packages\nmaintainers: [owner, second]\n")

		summary, output := reconcile(t, client, cfg, Options{Confirm: true})

		assert.Equal(t, 1, summary.ProjectsUpdated)
		assert.Equal(t, []string{"put project isv:kubernetes"}, client.mutatingCalls())
		assert.Equal(t, []string{
			"✎ project isv:kubernetes",
			`    title: "Kubernetes" → "Kubernetes Packages"`,
			"    maintainers: +second",
		}, lines(output))
	})
}

func TestReconcileIgnoresCosmeticDifferences(t *testing.T) {
	t.Parallel()

	client := newFakeClient()
	client.addProject(&obs.Project{
		Name:        "isv:kubernetes",
		Title:       "Kubernetes",
		Description: "A description.",
		Persons: []obs.Person{
			{UserID: "second", Role: obs.RoleMaintainer},
			{UserID: "owner", Role: obs.RoleMaintainer},
		},
	})

	// The description is a YAML block scalar, which adds a trailing newline
	// OBS never stores, and the maintainers are listed in another order.
	cfg := buildConfig(t, "name: isv:kubernetes\ntitle: Kubernetes\n"+
		"description: |\n  A description.\nmaintainers: [owner, second]\n")

	summary, output := reconcile(t, client, cfg, Options{Confirm: true})

	assert.False(t, summary.Changed())
	assert.Empty(t, client.mutatingCalls())
	assert.Empty(t, output)
}

// TestReconcileEveryFlagElement proves that every modelled flag makes the full
// round trip from configuration to OBS meta and back into the diff. A flag
// missing from the conversion would be silently cleared on the next apply.
func TestReconcileEveryFlagElement(t *testing.T) {
	t.Parallel()

	document := "name: isv:kubernetes\ntitle: Kubernetes\nmaintainers: [owner]\nflags:\n" +
		"  build: disable\n  publish: disable\n  useforbuild: disable\n  debuginfo: enable\n"

	live := &obs.Project{
		Name:        "isv:kubernetes",
		Title:       "Kubernetes",
		Persons:     []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		Build:       obs.Flag{{Action: obs.FlagActionDisable}},
		Publish:     obs.Flag{{Action: obs.FlagActionDisable}},
		UseForBuild: obs.Flag{{Action: obs.FlagActionDisable}},
		DebugInfo:   obs.Flag{{Action: obs.FlagActionEnable}},
	}

	t.Run("no change", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(live)

		summary, output := reconcile(t, client, buildConfig(t, document), Options{Confirm: true})

		assert.False(t, summary.Changed())
		assert.Empty(t, output)
	})

	t.Run("each flag is diffed individually", func(t *testing.T) {
		t.Parallel()

		for _, flag := range []string{"build", "publish", "useforbuild", "debuginfo"} {
			client := newFakeClient()

			dropped := *live
			client.addProject(&dropped)

			// Removing one flag from the configuration has to show up as that
			// flag being cleared, under its own OBS element name.
			changed := strings.Replace(document, "  "+flag+": disable\n", "", 1)
			changed = strings.Replace(changed, "  "+flag+": enable\n", "", 1)
			require.NotEqual(t, document, changed, flag)

			_, output := reconcile(t, client, buildConfig(t, changed), Options{Confirm: true})
			assert.Contains(t, output, flag+":", flag)
		}
	})
}

// TestReconcileDoesNotLookUpAncestors pins that reconciliation asks OBS only
// about the projects it was given. The shape of the declared tree is checked
// offline by config.Validate, so a run never probes a project it does not
// manage, no matter how deep the declared one sits.
func TestReconcileDoesNotLookUpAncestors(t *testing.T) {
	t.Parallel()

	client := newFakeClient()
	cfg := buildConfig(t,
		"name: isv:kubernetes:core:stable:v1.38\ntitle: Deep\nmaintainers: [owner]\n")

	summary, _ := reconcile(t, client, cfg, Options{Confirm: true})
	assert.Equal(t, 1, summary.ProjectsCreated)
	assert.Equal(t, []string{"isv:kubernetes:core:stable:v1.38"}, client.reads)
}

func TestReconcilePackages(t *testing.T) {
	t.Parallel()

	document := "name: isv:kubernetes\ntitle: Kubernetes\nmaintainers: [owner]\n" +
		"packages:\n  policy: %s\n  managed:\n    - name: kubelet\n    - name: kubeadm\n"

	t.Run("creates and updates", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(&obs.Project{
			Name: "isv:kubernetes", Title: "Kubernetes",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})
		client.addPackages("isv:kubernetes", "kubelet")
		client.packages["isv:kubernetes"]["kubelet"].Title = "Stale"

		cfg := buildConfig(t, sprintf(document, config.PolicyReport))

		summary, output := reconcile(t, client, cfg, Options{Confirm: true})

		assert.Equal(t, 1, summary.PackagesCreated)
		assert.Equal(t, 1, summary.PackagesUpdated)
		assert.Equal(t, []string{
			"  ✎ package isv:kubernetes/kubelet",
			`      title: "Stale" → ""`,
			"  + package isv:kubernetes/kubeadm",
		}, lines(output))
	})

	t.Run("reports unmanaged packages", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(&obs.Project{
			Name: "isv:kubernetes", Title: "Kubernetes",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})
		client.addPackages("isv:kubernetes", "kubelet", "kubeadm", "leftover")

		cfg := buildConfig(t, sprintf(document, config.PolicyReport))

		summary, output := reconcile(t, client, cfg, Options{Confirm: true})

		assert.Equal(t, 1, summary.Reported)
		assert.Equal(t, 0, summary.PackagesDeleted)
		assert.Empty(t, client.mutatingCalls())
		assert.Contains(t, output, "! package isv:kubernetes/leftover")
	})

	t.Run("ignores unmanaged packages", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(&obs.Project{
			Name: "isv:kubernetes", Title: "Kubernetes",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})
		client.addPackages("isv:kubernetes", "kubelet", "kubeadm", "leftover")

		cfg := buildConfig(t, sprintf(document, config.PolicyIgnore))

		summary, output := reconcile(t, client, cfg, Options{Confirm: true})

		assert.Equal(t, 0, summary.Reported)
		assert.Empty(t, output)
	})

	t.Run("prune needs allow deletions", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(&obs.Project{
			Name: "isv:kubernetes", Title: "Kubernetes",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})
		client.addPackages("isv:kubernetes", "kubelet", "kubeadm", "leftover")

		cfg := buildConfig(t, sprintf(document, config.PolicyPrune))

		summary, output := reconcile(t, client, cfg, Options{Confirm: true})

		assert.Equal(t, 1, summary.Reported)
		assert.Equal(t, 0, summary.PackagesDeleted)
		assert.Empty(t, client.mutatingCalls())
		assert.Contains(t, output, "--allow-deletions")
		assert.Contains(t, output, "! package isv:kubernetes/leftover")
	})

	t.Run("prune deletes when allowed", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(&obs.Project{
			Name: "isv:kubernetes", Title: "Kubernetes",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})
		client.addPackages("isv:kubernetes", "kubelet", "kubeadm", "leftover")

		cfg := buildConfig(t, sprintf(document, config.PolicyPrune))

		summary, output := reconcile(t, client, cfg, Options{Confirm: true, AllowDeletions: true})

		assert.Equal(t, 1, summary.PackagesDeleted)
		assert.Equal(t, []string{"delete package isv:kubernetes/leftover"}, client.mutatingCalls())
		assert.Contains(t, output, "- package isv:kubernetes/leftover")
		assert.NotContains(t, client.packages["isv:kubernetes"], "leftover")
	})
}

func TestReconcilePackageLogsIdentifyProjects(t *testing.T) {
	t.Parallel()

	client := newFakeClient()
	cfg := buildConfig(t,
		"name: isv:kubernetes:b\nmaintainers: [owner]\n"+
			"packages:\n  managed:\n    - name: kubelet\n      title: New\n",
		"name: isv:kubernetes:a\nmaintainers: [owner]\n"+
			"packages:\n  managed:\n    - name: kubelet\n      title: New\n",
	)
	require.NoError(t, config.Validate(cfg, config.ValidateOptions{}))

	for _, project := range cfg.Projects {
		client.addProject(ProjectMeta(project))
		client.addPackages(project.Name, "kubelet")
		client.packages[project.Name]["kubelet"].Title = "Old"
	}

	summary, output := reconcile(t, client, cfg, Options{})
	assert.Equal(t, 2, summary.PackagesUpdated)
	assert.Zero(t, summary.ProjectsUpdated)
	assert.Empty(t, client.mutatingCalls())
	assert.Equal(t, []string{
		"  ✎ package isv:kubernetes:a/kubelet",
		`      title: "Old" → "New"`,
		"  ✎ package isv:kubernetes:b/kubelet",
		`      title: "Old" → "New"`,
	}, lines(output))
}

func TestReconcileDefersPackageDeletionsUntilAfterUpdates(t *testing.T) {
	t.Parallel()

	for name, allowDeletions := range map[string]bool{"deletions allowed": true, "deletions blocked": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := buildConfig(t,
				"name: isv:kubernetes:a\nmaintainers: [owner]\npackages:\n  policy: prune\n"+
					"subprojects:\n  policy: prune\n",
				"name: isv:kubernetes:b\ntitle: New\nmaintainers: [owner]\npackages:\n  managed:\n"+
					"    - name: kubelet\n      title: New\n"+
					"    - name: created\n",
			)
			require.NoError(t, config.Validate(cfg, config.ValidateOptions{}))

			newClient := func() *fakeClient {
				client := newFakeClient()
				for _, project := range cfg.Projects {
					client.addProject(ProjectMeta(project))
				}

				client.projects["isv:kubernetes:b"].Title = "Old"
				client.addPackages("isv:kubernetes:a", "obsolete-z", "obsolete-a")
				client.addPackages("isv:kubernetes:b", "kubelet")
				client.packages["isv:kubernetes:b"]["kubelet"].Title = "Old"
				client.addProject(&obs.Project{Name: "isv:kubernetes:a:obsolete"})
				client.subprojects["isv:kubernetes:a"] = []string{"isv:kubernetes:a:obsolete"}

				return client
			}

			dryClient := newClient()
			drySummary, dryOutput := reconcile(t, dryClient, cfg, Options{AllowDeletions: allowDeletions})
			assert.Empty(t, dryClient.mutatingCalls())
			assert.Contains(t, dryClient.packages["isv:kubernetes:a"], "obsolete-a")
			assert.Contains(t, dryClient.packages["isv:kubernetes:a"], "obsolete-z")

			client := newClient()
			summary, output := reconcile(t, client, cfg, Options{
				Confirm: true, AllowDeletions: allowDeletions,
			})
			assert.Equal(t, drySummary, summary)
			assert.Equal(t, dryOutput, output)
			assert.Equal(t, 1, summary.ProjectsUpdated)
			assert.Equal(t, 1, summary.PackagesUpdated)
			assert.Equal(t, 1, summary.PackagesCreated)

			expected := []string{
				"put project isv:kubernetes:b",
				"put package isv:kubernetes:b/kubelet",
				"put package isv:kubernetes:b/created",
			}
			if allowDeletions {
				expected = append(expected,
					"delete package isv:kubernetes:a/obsolete-a",
					"delete package isv:kubernetes:a/obsolete-z",
					"delete project isv:kubernetes:a:obsolete",
				)

				assert.Equal(t, 2, summary.PackagesDeleted)
				assert.Equal(t, 1, summary.ProjectsDeleted)
				assert.Zero(t, summary.Reported)
				require.Contains(t, output, "+ package isv:kubernetes:b/created")
				require.Contains(t, output, "- package isv:kubernetes:a/obsolete-a")
				require.Contains(t, output, "- package isv:kubernetes:a/obsolete-z")
				require.Contains(t, output, "- project isv:kubernetes:a:obsolete")
				assert.Less(t,
					strings.Index(output, "+ package isv:kubernetes:b/created"),
					strings.Index(output, "- package isv:kubernetes:a/obsolete-a"),
				)
				assert.Less(t,
					strings.Index(output, "- package isv:kubernetes:a/obsolete-z"),
					strings.Index(output, "- project isv:kubernetes:a:obsolete"),
				)
				assert.Empty(t, client.packages["isv:kubernetes:a"])
			} else {
				assert.Zero(t, summary.PackagesDeleted)
				assert.Zero(t, summary.ProjectsDeleted)
				assert.Equal(t, 3, summary.Reported)
				assert.Contains(t, output, "! package isv:kubernetes:a/obsolete-a")
				assert.Contains(t, client.packages["isv:kubernetes:a"], "obsolete-a")
				assert.Contains(t, client.packages["isv:kubernetes:a"], "obsolete-z")
			}

			assert.Equal(t, expected, client.mutatingCalls())
		})
	}
}

func TestReconcileDoesNotDeletePackagesWhenLaterMutationFails(t *testing.T) {
	t.Parallel()

	for _, stage := range []string{"project update", "package creation", "package update"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()

			client := newFakeClient()
			cfg := buildConfig(t,
				"name: isv:kubernetes:a\nmaintainers: [owner]\npackages:\n  policy: prune\n",
				"name: isv:kubernetes:b\ntitle: New\nmaintainers: [owner]\npackages:\n  managed:\n"+
					"    - name: kubelet\n      title: New\n",
			)
			require.NoError(t, config.Validate(cfg, config.ValidateOptions{}))

			for _, project := range cfg.Projects {
				client.addProject(ProjectMeta(project))
			}

			client.addPackages("isv:kubernetes:a", "leftover")
			client.mutationErr = errors.New("mutation failed")

			expectedCall := "put package isv:kubernetes:b/kubelet"

			switch stage {
			case "project update":
				client.projects["isv:kubernetes:b"].Title = "Old"
				expectedCall = "put project isv:kubernetes:b"
			case "package update":
				client.addPackages("isv:kubernetes:b", "kubelet")
				client.packages["isv:kubernetes:b"]["kubelet"].Title = "Old"
			}

			output := &bytes.Buffer{}
			summary, err := Reconcile(t.Context(), client, cfg, NewLogger(output), Options{
				Confirm: true, AllowDeletions: true,
			})

			require.ErrorContains(t, err, "mutation failed")
			assert.Equal(t, []string{expectedCall}, client.mutatingCalls())
			assert.Equal(t, Summary{}, summary)
			assert.Contains(t, client.packages["isv:kubernetes:a"], "leftover")
			assert.NotContains(t, output.String(), "- package")
		})
	}
}

// TestReconcileNeverTouchesReleaseCopies pins that the timestamped copies the
// OBS release mechanism creates are invisible to obscli, whatever the policy.
func TestReconcileNeverTouchesReleaseCopies(t *testing.T) {
	t.Parallel()

	client := newFakeClient()
	client.addProject(&obs.Project{
		Name: "isv:kubernetes", Title: "Kubernetes",
		Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
	})
	client.addPackages("isv:kubernetes",
		"kubelet",
		"kubelet.20260923195931",
		"kubernetes-cni.20260424110236.20260424140455",
	)

	cfg := buildConfig(t, "name: isv:kubernetes\ntitle: Kubernetes\nmaintainers: [owner]\n"+
		"packages:\n  policy: prune\n  managed:\n    - name: kubelet\n")

	summary, output := reconcile(t, client, cfg, Options{Confirm: true, AllowDeletions: true})

	assert.Equal(t, 0, summary.PackagesDeleted)
	assert.Equal(t, 0, summary.Reported)
	assert.Empty(t, client.mutatingCalls())
	assert.Empty(t, output)
}

func TestReconcileProjectPruning(t *testing.T) {
	t.Parallel()

	newClient := func() *fakeClient {
		client := newFakeClient()
		client.addProject(&obs.Project{
			Name: "isv:kubernetes", Title: "Kubernetes",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})
		client.addProject(&obs.Project{
			Name: "isv:kubernetes:core", Title: "Core",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})
		client.addProject(&obs.Project{Name: "isv:kubernetes:obsolete"})
		client.addProject(&obs.Project{Name: "isv:kubernetes:obsolete:child"})
		client.subprojects["isv:kubernetes"] = []string{
			"isv:kubernetes:core", "isv:kubernetes:obsolete", "isv:kubernetes:obsolete:child",
		}

		return client
	}

	// documents builds the two declared projects, giving the root the policy
	// under test.
	documents := func(policy string) []string {
		root := "name: isv:kubernetes\ntitle: Kubernetes\nmaintainers: [owner]\n"
		if policy != "" {
			root += "subprojects:\n  policy: " + policy + "\n"
		}

		return []string{root, "name: isv:kubernetes:core\ntitle: Core\nmaintainers: [owner]\n"}
	}

	t.Run("reports by default", func(t *testing.T) {
		t.Parallel()

		client := newClient()

		summary, output := reconcile(t, client, buildConfig(t, documents("")...), Options{Confirm: true})

		assert.Equal(t, 2, summary.Reported)
		assert.Equal(t, 0, summary.ProjectsDeleted)
		assert.Contains(t, output, "! project isv:kubernetes:obsolete")
	})

	t.Run("ignores when asked to", func(t *testing.T) {
		t.Parallel()

		client := newClient()

		summary, _ := reconcile(t, client, buildConfig(t, documents("ignore")...), Options{Confirm: true})

		assert.Equal(t, 0, summary.Reported)
		assert.Empty(t, client.mutatingCalls())

		// Nothing is even enumerated, so an ignoring run costs no API call.
		assert.NotContains(t, client.calls, "list subprojects isv:kubernetes")
	})

	t.Run("deletes children before parents", func(t *testing.T) {
		t.Parallel()

		client := newClient()

		summary, _ := reconcile(t, client, buildConfig(t, documents("prune")...), Options{
			Confirm:        true,
			AllowDeletions: true,
		})

		assert.Equal(t, 2, summary.ProjectsDeleted)
		assert.Equal(t, []string{
			"delete project isv:kubernetes:obsolete:child",
			"delete project isv:kubernetes:obsolete",
		}, client.mutatingCalls())
	})

	t.Run("prune needs allow deletions", func(t *testing.T) {
		t.Parallel()

		client := newClient()

		summary, output := reconcile(t, client, buildConfig(t, documents("prune")...),
			Options{Confirm: true})

		assert.Equal(t, 2, summary.Reported)
		assert.Equal(t, 0, summary.ProjectsDeleted)
		assert.Empty(t, client.mutatingCalls())
		assert.Contains(t, output, "--allow-deletions")
	})

	// A run only looks below the projects it declares, so it can never reach a
	// sibling subtree.
	t.Run("scoped run only sees its own subtree", func(t *testing.T) {
		t.Parallel()

		client := newClient()
		client.subprojects["isv:kubernetes:core"] = nil

		summary, _ := reconcile(t, client, buildConfig(t, documents("prune")[1]), Options{
			Confirm:        true,
			AllowDeletions: true,
		})

		assert.Equal(t, 0, summary.ProjectsDeleted)
		assert.Contains(t, client.calls, "list subprojects isv:kubernetes:core")
		assert.NotContains(t, client.calls, "list subprojects isv:kubernetes")
	})

	// The policy of the nearest declared ancestor wins, which is what makes
	// per subtree control possible.
	t.Run("nearest declared ancestor governs", func(t *testing.T) {
		t.Parallel()

		client := newClient()
		client.addProject(&obs.Project{
			Name: "isv:kubernetes:addons", Title: "Addons",
			Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		})
		client.addProject(&obs.Project{Name: "isv:kubernetes:addons:leftover"})
		client.subprojects["isv:kubernetes"] = append(
			client.subprojects["isv:kubernetes"],
			"isv:kubernetes:addons", "isv:kubernetes:addons:leftover",
		)

		// The root prunes, but the addons subtree is declared as untouchable.
		cfg := buildConfig(t, append(documents("prune"),
			"name: isv:kubernetes:addons\ntitle: Addons\nmaintainers: [owner]\n"+
				"subprojects:\n  policy: ignore\n")...)

		summary, _ := reconcile(t, client, cfg, Options{Confirm: true, AllowDeletions: true})

		assert.Equal(t, 2, summary.ProjectsDeleted)
		assert.Equal(t, []string{
			"delete project isv:kubernetes:obsolete:child",
			"delete project isv:kubernetes:obsolete",
		}, client.mutatingCalls())
		assert.Contains(t, client.projects, "isv:kubernetes:addons:leftover")
	})
}

func TestPruneRoots(t *testing.T) {
	t.Parallel()

	t.Run("only the topmost looking project is searched", func(t *testing.T) {
		t.Parallel()

		cfg := buildConfig(t,
			"name: isv:kubernetes:core\n",
			"name: isv:kubernetes\n",
			"name: isv:kubernetes:core:stable\n",
			"name: other:tree\n",
		)

		assert.Equal(t, []string{"isv:kubernetes", "other:tree"}, pruneRoots(cfg.Projects))
	})

	t.Run("an ignoring ancestor does not cover its children", func(t *testing.T) {
		t.Parallel()

		cfg := buildConfig(t,
			"name: isv:kubernetes\nsubprojects:\n  policy: ignore\n",
			"name: isv:kubernetes:core\nsubprojects:\n  policy: prune\n",
		)

		assert.Equal(t, []string{"isv:kubernetes:core"}, pruneRoots(cfg.Projects))
	})

	t.Run("nothing is searched when everything ignores", func(t *testing.T) {
		t.Parallel()

		cfg := buildConfig(t,
			"name: isv:kubernetes\nsubprojects:\n  policy: ignore\n",
			"name: isv:kubernetes:core\nsubprojects:\n  policy: ignore\n",
		)

		assert.Empty(t, pruneRoots(cfg.Projects))
	})
}

// reconcile runs a reconciliation and returns its summary and log output.
func reconcile(t *testing.T, client Client, cfg *config.Config, options Options) (summary Summary, logs string) {
	t.Helper()

	output := &bytes.Buffer{}

	summary, err := Reconcile(t.Context(), client, cfg, NewLogger(output), options)
	require.NoError(t, err)

	return summary, output.String()
}

// lines splits log output into its non empty lines.
func lines(output string) []string {
	var result []string

	for line := range strings.SplitSeq(output, "\n") {
		if strings.TrimSpace(line) != "" {
			result = append(result, line)
		}
	}

	return result
}

// sprintf is a tiny helper keeping the table driven tests readable.
func sprintf(format string, policy config.Policy) string {
	return strings.Replace(format, "%s", string(policy), 1)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("output failed")
}
