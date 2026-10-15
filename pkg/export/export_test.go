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
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/release-sdk/obs"

	"sigs.k8s.io/obscli/pkg/config"
	"sigs.k8s.io/obscli/pkg/sync"
)

// TestLayoutFollowsTheDocumentedConvention pins the file layout against the
// example in docs/design.md.
func TestLayoutFollowsTheDocumentedConvention(t *testing.T) {
	t.Parallel()

	projects := configProjects(
		"isv:kubernetes",
		"isv:kubernetes:core",
		"isv:kubernetes:core:stable",
		"isv:kubernetes:core:stable:v1.34",
		"isv:kubernetes:core:stable:v1.34:build",
		"isv:kubernetes:core:prerelease",
		"isv:kubernetes:core:prerelease:v1.35",
		"isv:kubernetes:core:prerelease:v1.35:build",
		"isv:kubernetes:addons",
		"isv:kubernetes:addons:cri-o",
	)

	layout, err := Layout("isv:kubernetes", projects)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"isv:kubernetes":                             "kubernetes.yaml",
		"isv:kubernetes:core":                        "core.yaml",
		"isv:kubernetes:core:stable":                 filepath.Join("core", "stable.yaml"),
		"isv:kubernetes:core:stable:v1.34":           filepath.Join("core", "stable", "v1.34.yaml"),
		"isv:kubernetes:core:stable:v1.34:build":     filepath.Join("core", "stable", "v1.34.build.yaml"),
		"isv:kubernetes:core:prerelease":             filepath.Join("core", "prerelease.yaml"),
		"isv:kubernetes:core:prerelease:v1.35":       filepath.Join("core", "prerelease", "v1.35.yaml"),
		"isv:kubernetes:core:prerelease:v1.35:build": filepath.Join("core", "prerelease", "v1.35.build.yaml"),

		// addons has children but no grandchild, so it stays a plain file and
		// its child is flattened next to it rather than into a directory.
		"isv:kubernetes:addons":       "addons.yaml",
		"isv:kubernetes:addons:cri-o": "addons.cri-o.yaml",
	}, layout)
}

func TestLayoutWithASingleProject(t *testing.T) {
	t.Parallel()

	projects := configProjects("isv:kubernetes:core:stable:v1.34")

	layout, err := Layout("isv:kubernetes:core:stable:v1.34", projects)
	require.NoError(t, err)

	assert.Equal(t,
		map[string]string{"isv:kubernetes:core:stable:v1.34": "v1.34.yaml"},
		layout,
	)
}

func TestLayoutRejectsUnsafeProjects(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		root     string
		projects []*config.Project
		errorIs  string
	}{
		"invalid root": {
			root:     "../kubernetes",
			projects: configProjects("isv:kubernetes"),
			errorIs:  "not a valid OBS project name",
		},
		"invalid project": {
			root:     "isv:kubernetes",
			projects: configProjects("isv:kubernetes:../outside"),
			errorIs:  "not a valid OBS project name",
		},
		"unrelated project": {
			root:     "isv:kubernetes",
			projects: configProjects("other:project"),
			errorIs:  "not \"isv:kubernetes\" or one of its subprojects",
		},
		"duplicate project": {
			root:     "isv:kubernetes",
			projects: configProjects("isv:kubernetes", "isv:kubernetes"),
			errorIs:  "exported more than once",
		},
		"dot collision": {
			root: "isv:kubernetes",
			projects: configProjects(
				"isv:kubernetes:a:b",
				"isv:kubernetes:a.b",
			),
			errorIs: "both map to",
		},
		"root collision": {
			root: "isv:kubernetes:b",
			projects: configProjects(
				"isv:kubernetes:b",
				"isv:kubernetes:b:b",
			),
			errorIs: "both map to",
		},
		"case insensitive collision": {
			root: "isv:kubernetes",
			projects: configProjects(
				"isv:kubernetes:Core",
				"isv:kubernetes:core",
			),
			errorIs: "both map to",
		},
		"file and directory collision": {
			root: "isv:kubernetes",
			projects: configProjects(
				"isv:kubernetes:core",
				"isv:kubernetes:core.yaml:a",
				"isv:kubernetes:core.yaml:a:b",
			),
			errorIs: "maps that path to a file",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := Layout(test.root, test.projects)
			require.ErrorContains(t, err, test.errorIs)
		})
	}
}

// TestMarshalOmitsEverythingEmpty guards the readability of generated files:
// a project carrying only a name and a maintainer must not grow a single empty
// key, and a flag holding one unscoped rule must render as a plain string.
func TestMarshalOmitsEverythingEmpty(t *testing.T) {
	t.Parallel()

	project := &config.Project{
		Name:   "isv:kubernetes:core:stable:v1.34",
		Title:  "Kubernetes 1.34",
		People: config.People{Maintainers: []string{"owner"}},
		Flags:  config.Flags{Build: config.Flag{{Action: obs.FlagActionDisable}}},
		Packages: config.Packages{
			Managed: []config.Package{{Name: "kubelet"}},
		},
	}

	rendered, err := Marshal(project)
	require.NoError(t, err)

	assert.Equal(t, `name: isv:kubernetes:core:stable:v1.34
title: Kubernetes 1.34
maintainers:
  - owner
flags:
  build: disable
packages:
  managed:
    - name: kubelet
`, string(rendered))
}

func TestMarshalRendersScopedFlagsAsList(t *testing.T) {
	t.Parallel()

	project := &config.Project{
		Name: "isv:kubernetes",
		Flags: config.Flags{
			Publish: config.Flag{
				{Action: obs.FlagActionDisable},
				{Action: obs.FlagActionEnable, Repository: "standard", Arch: "x86_64"},
			},
		},
	}

	rendered, err := Marshal(project)
	require.NoError(t, err)

	assert.Equal(t, `name: isv:kubernetes
flags:
  publish:
    - disable
    - enable:
        repository: standard
        arch: x86_64
`, string(rendered))
}

func TestExport(t *testing.T) {
	t.Parallel()

	client := liveTree()

	t.Run("single project", func(t *testing.T) {
		t.Parallel()

		projects, err := Export(t.Context(), client, Options{Project: "isv:kubernetes:core"})
		require.NoError(t, err)

		require.Len(t, projects, 1)
		assert.Equal(t, "isv:kubernetes:core", projects[0].Name)
		assert.Empty(t, projects[0].Packages.Managed)
	})

	t.Run("recursive, parents first", func(t *testing.T) {
		t.Parallel()

		projects, err := Export(t.Context(), client,
			Options{Project: "isv:kubernetes", Recursive: true})
		require.NoError(t, err)

		assert.Equal(t, []string{
			"isv:kubernetes",
			"isv:kubernetes:core",
			"isv:kubernetes:core:stable",
		}, names(projects))
	})

	t.Run("with packages, without release copies", func(t *testing.T) {
		t.Parallel()

		projects, err := Export(t.Context(), client,
			Options{Project: "isv:kubernetes:core:stable", WithPackages: true})
		require.NoError(t, err)

		require.Len(t, projects, 1)
		assert.Equal(t, []config.Package{
			{Name: "kubeadm"},
			{Name: "kubelet", Title: "The kubelet"},
		}, projects[0].Packages.Managed)
	})

	// Exporting a namespace would render it as a project, producing a
	// configuration that obscli refuses to load back.
	t.Run("refuses a top level name", func(t *testing.T) {
		t.Parallel()

		_, err := Export(t.Context(), client, Options{Project: "isv", Recursive: true})
		require.ErrorContains(t, err, "is a top level name, which obscli never manages")
	})
}

func TestExportRejectsUntrustedResponseNames(t *testing.T) {
	t.Parallel()

	t.Run("invalid requested project", func(t *testing.T) {
		t.Parallel()

		_, err := Export(t.Context(), newFakeClient(), Options{Project: "isv:kubernetes/other"})
		require.ErrorContains(t, err, "not a valid OBS project name")
	})

	t.Run("unrelated recursive result", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(&obs.Project{Name: "isv:kubernetes"})
		client.subprojects["isv:kubernetes"] = []string{"other:project"}

		_, err := Export(t.Context(), client, Options{Project: "isv:kubernetes", Recursive: true})
		require.ErrorContains(t, err, `OBS returned unrelated project "other:project"`)
	})

	t.Run("project metadata mismatch", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.projects["isv:kubernetes"] = &obs.Project{Name: "isv:kubernetes:other"}

		_, err := Export(t.Context(), client, Options{Project: "isv:kubernetes"})
		require.ErrorContains(t, err, `OBS returned metadata for "isv:kubernetes:other"`)
	})

	t.Run("package metadata mismatch", func(t *testing.T) {
		t.Parallel()

		client := newFakeClient()
		client.addProject(&obs.Project{Name: "isv:kubernetes"})
		client.packages["isv:kubernetes"] = map[string]*obs.Package{
			"kubelet": {Name: "kubectl", Project: "isv:kubernetes"},
		}

		_, err := Export(t.Context(), client, Options{
			Project:      "isv:kubernetes",
			WithPackages: true,
		})
		require.ErrorContains(t, err, "OBS returned metadata for isv:kubernetes/kubectl")
	})
}

func TestExportConvertsTheFullMeta(t *testing.T) {
	t.Parallel()

	project := ProjectConfig(&obs.Project{
		Name:        "isv:kubernetes:core:stable:v1.34",
		Kind:        obs.ProjectKindStandard,
		Title:       "Kubernetes 1.34  ",
		Description: "Packages.\n",
		Persons: []obs.Person{
			{UserID: "second", Role: obs.RoleMaintainer},
			{UserID: "owner", Role: obs.RoleMaintainer},
			{UserID: "reporter", Role: obs.RoleBugOwner},
		},
		Groups:  []obs.Group{{GroupID: "team", Role: obs.RoleReviewer}},
		Publish: obs.Flag{{Action: obs.FlagActionDisable}},
		Repositories: []obs.Repository{{
			Name:          "standard",
			Architectures: []string{"x86_64", "aarch64"},
			Paths: []obs.RepositoryPath{
				{Project: "openSUSE:Factory", Repository: "snapshot"},
			},
			ReleaseTargets: []obs.ReleaseTarget{
				{Project: "isv:kubernetes:core:stable:v1.34", Repository: "rpm", Trigger: obs.ReleaseTriggerManual},
			},
		}},
	})

	assert.Equal(t, &config.Project{
		Name: "isv:kubernetes:core:stable:v1.34",

		// A standard project has no kind in the configuration, and OBS never
		// returns a trailing newline the way a YAML block scalar produces one.
		Kind:        "",
		Title:       "Kubernetes 1.34",
		Description: "Packages.",

		People: config.People{
			Maintainers: []string{"owner", "second"},
			Bugowners:   []string{"reporter"},
		},
		Groups: config.People{Reviewers: []string{"team"}},
		Flags:  config.Flags{Publish: config.Flag{{Action: obs.FlagActionDisable}}},
		Repositories: []config.Repository{{
			Name:          "standard",
			Architectures: []string{"x86_64", "aarch64"},
			Paths: []config.RepositoryPath{
				{Project: "openSUSE:Factory", Repository: "snapshot"},
			},
			ReleaseTargets: []config.ReleaseTarget{
				{Project: "isv:kubernetes:core:stable:v1.34", Repository: "rpm", Trigger: obs.ReleaseTriggerManual},
			},
		}},
	}, project)
}

// TestRoundTrip is the guarantee that makes export usable for adopting an
// existing tree: exporting the live state, writing it out, loading it back and
// applying it must not produce a single change.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	client := liveTree()

	projects, err := Export(t.Context(), client,
		Options{Project: "isv:kubernetes", Recursive: true, WithPackages: true})
	require.NoError(t, err)

	dir := t.TempDir()
	_, err = Write(projects, "isv:kubernetes", dir)
	require.NoError(t, err)

	loaded, err := config.Load(dir)
	require.NoError(t, err)

	require.NoError(t, config.Validate(loaded, config.ValidateOptions{}))
	assert.ElementsMatch(t, names(projects), names(loaded.Projects))

	summary, err := sync.Reconcile(t.Context(), client, loaded,
		sync.NewLogger(io.Discard), sync.Options{Confirm: true})
	require.NoError(t, err)

	assert.False(t, summary.Changed(), "applying an export must be a no-op")
	assert.Equal(t, 0, summary.Reported)
	assert.Empty(t, client.writes)
}

func TestWriteCreatesTheLayout(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	projects := configProjects(
		"isv:kubernetes",
		"isv:kubernetes:core",
		"isv:kubernetes:core:stable",
		"isv:kubernetes:core:stable:v1.34",
		"isv:kubernetes:core:stable:v1.34:build",
	)

	_, err := Write(projects, "isv:kubernetes", dir)
	require.NoError(t, err)

	var found []string

	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.IsDir() {
			relative, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}

			found = append(found, relative)
		}

		return nil
	}))

	assert.ElementsMatch(t, []string{
		"kubernetes.yaml",
		"core.yaml",
		filepath.Join("core", "stable.yaml"),
		filepath.Join("core", "stable", "v1.34.yaml"),
		filepath.Join("core", "stable", "v1.34.build.yaml"),
	}, found)
}

func TestCheckOutputDirDoesNotCreateDirectory(t *testing.T) {
	t.Parallel()

	outputDir := filepath.Join(t.TempDir(), "new", "export")
	require.NoError(t, CheckOutputDir(outputDir))
	assert.NoDirExists(t, filepath.Dir(outputDir))
}

func TestWriteCreatesMissingOutputDirectory(t *testing.T) {
	t.Parallel()

	outputDir := filepath.Join(t.TempDir(), "new", "export")
	_, err := Write(configProjects("isv:kubernetes"), "isv:kubernetes", outputDir)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(outputDir, "kubernetes.yaml"))
}

func TestWriteRejectsNonEmptyOutputDirectory(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		entry     string
		directory bool
	}{
		"existing export":  {entry: "kubernetes.yaml"},
		"unrelated file":   {entry: "README.txt"},
		"hidden file":      {entry: ".keep"},
		"empty directory":  {entry: "empty", directory: true},
		"hidden directory": {entry: ".git", directory: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			outputDir := t.TempDir()

			existing := filepath.Join(outputDir, testCase.entry)
			if testCase.directory {
				require.NoError(t, os.Mkdir(existing, 0o755))
			} else {
				require.NoError(t, os.WriteFile(existing, []byte("preserve me\n"), 0o600))
			}

			layout, err := Write(
				configProjects("isv:kubernetes", "isv:kubernetes:core"),
				"isv:kubernetes", outputDir,
			)
			require.ErrorContains(t, err, "is not empty; choose a new or empty directory")
			assert.Nil(t, layout)

			entries, err := os.ReadDir(outputDir)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, testCase.entry, entries[0].Name())

			if testCase.directory {
				contents, err := os.ReadDir(existing)
				require.NoError(t, err)
				assert.Empty(t, contents)
			} else {
				content, err := os.ReadFile(existing)
				require.NoError(t, err)
				assert.Equal(t, "preserve me\n", string(content))
			}
		})
	}
}

func TestWriteRejectsFileAsOutputDirectory(t *testing.T) {
	t.Parallel()

	output := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(output, []byte("preserve me\n"), 0o600))

	_, err := Write(configProjects("isv:kubernetes"), "isv:kubernetes", output)
	require.ErrorContains(t, err, "reading output directory")

	content, err := os.ReadFile(output)
	require.NoError(t, err)
	assert.Equal(t, "preserve me\n", string(content))
}

func TestWritePlansEverythingBeforeCreatingFiles(t *testing.T) {
	t.Parallel()

	outputDir := filepath.Join(t.TempDir(), "export")
	projects := configProjects(
		"isv:kubernetes:a:b",
		"isv:kubernetes:a.b",
	)

	_, err := Write(projects, "isv:kubernetes", outputDir)
	require.ErrorContains(t, err, "both map to")
	assert.NoDirExists(t, outputDir)
}

func TestWriteRejectsDestinationSymlink(t *testing.T) {
	t.Parallel()

	for name, targetExists := range map[string]bool{"existing target": true, "dangling symlink": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()

			target := filepath.Join(dir, "target.yaml")
			if targetExists {
				require.NoError(t, os.WriteFile(target, []byte("do not replace\n"), 0o600))
			}

			outputDir := filepath.Join(dir, "export")
			require.NoError(t, os.MkdirAll(outputDir, 0o755))

			destination := filepath.Join(outputDir, "kubernetes.yaml")
			if err := os.Symlink(target, destination); err != nil {
				t.Skipf("creating symlink: %v", err)
			}

			_, err := Write(configProjects("isv:kubernetes"), "isv:kubernetes", outputDir)
			require.ErrorContains(t, err, "is not empty; choose a new or empty directory")

			info, err := os.Lstat(destination)
			require.NoError(t, err)
			assert.Equal(t, os.ModeSymlink, info.Mode()&os.ModeSymlink)

			if targetExists {
				content, err := os.ReadFile(target)
				require.NoError(t, err)
				assert.Equal(t, "do not replace\n", string(content))
			} else {
				assert.NoFileExists(t, target)
			}
		})
	}
}

// liveTree is a small but complete OBS instance: a hierarchy, people, flags,
// repositories, packages and a release copy.
func liveTree() *fakeClient {
	client := newFakeClient()

	// isv is the unmanaged top level namespace the tree lives under.
	client.addProject(&obs.Project{Name: "isv"})

	client.addProject(&obs.Project{
		Name:        "isv:kubernetes",
		Title:       "Kubernetes",
		Description: "Kubernetes packages.",
		Persons:     []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
	})
	client.addProject(&obs.Project{
		Name:    "isv:kubernetes:core",
		Title:   "Core",
		Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		Build:   obs.Flag{{Action: obs.FlagActionDisable}},
	})
	client.addProject(&obs.Project{
		Name:    "isv:kubernetes:core:stable",
		Title:   "Stable",
		Persons: []obs.Person{{UserID: "owner", Role: obs.RoleMaintainer}},
		Repositories: []obs.Repository{{
			Name:          "standard",
			Architectures: []string{"x86_64", "aarch64"},
			Paths: []obs.RepositoryPath{
				{Project: "openSUSE:Factory", Repository: "snapshot"},
			},
		}},
	})

	client.addPackage("isv:kubernetes:core:stable", &obs.Package{
		Name: "kubelet", Project: "isv:kubernetes:core:stable", Title: "The kubelet",
	})
	client.addPackage("isv:kubernetes:core:stable", &obs.Package{
		Name: "kubeadm", Project: "isv:kubernetes:core:stable",
	})
	client.addPackage("isv:kubernetes:core:stable", &obs.Package{
		Name: "kubelet.20260923195931", Project: "isv:kubernetes:core:stable",
	})

	return client
}

// configProjects builds bare configuration projects from their names.
func configProjects(names ...string) []*config.Project {
	projects := make([]*config.Project, 0, len(names))
	for _, name := range names {
		projects = append(projects, &config.Project{Name: name})
	}

	return projects
}

// names returns the project names in order.
func names(projects []*config.Project) []string {
	result := make([]string, 0, len(projects))
	for _, project := range projects {
		result = append(result, project.Name)
	}

	return result
}
