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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/release-sdk/obs"
)

func TestLoadDirectory(t *testing.T) {
	t.Parallel()

	config, err := Load(filepath.Join("testdata", "valid"))
	require.NoError(t, err)
	require.Len(t, config.Projects, 5)

	// Files are loaded in a stable, path sorted order.
	assert.Equal(t, []string{
		"isv:kubernetes:core",
		"isv:kubernetes:core:stable",
		"isv:kubernetes:core:stable:v1.34:build",
		"isv:kubernetes:core:stable:v1.34",
		"isv:kubernetes",
	}, projectNames(config))

	for _, project := range config.Projects {
		assert.NotEmpty(t, project.SourcePath())
	}
}

func TestLoadSingleFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join("testdata", "valid", "kubernetes.yaml")

	config, err := Load(path)
	require.NoError(t, err)
	require.Len(t, config.Projects, 1)

	project := config.Projects[0]
	assert.Equal(t, "isv:kubernetes", project.Name)
	assert.Equal(t, path, project.SourcePath())
	assert.Equal(t, "Kubernetes", project.Title)
	assert.Equal(t, "https://kubernetes.io", project.URL)
	assert.Equal(t, []string{"k8s-release-bot", "xmudrii"}, project.People.Maintainers)
	assert.Equal(t, []string{"k8s-release-bot", "xmudrii"}, project.People.Bugowners)
	assert.Equal(t, Flag{{Action: obs.FlagActionDisable}}, project.Flags.Build)
	assert.Equal(t, Flag{{Action: obs.FlagActionDisable}}, project.Flags.UseForBuild)

	// An omitted packages block defaults to the safe report policy.
	assert.Equal(t, PolicyReport, project.Packages.PolicyOrDefault())
	assert.Empty(t, project.Packages.Managed)
}

func TestLoadBuilderProject(t *testing.T) {
	t.Parallel()

	project, err := LoadFile(filepath.Join("testdata", "valid", "core", "stable", "v1.34.build.yaml"))
	require.NoError(t, err)

	assert.Equal(t, "isv:kubernetes:core:stable:v1.34:build", project.Name)
	assert.Equal(t, []string{"k8s-release-team"}, project.Groups.Maintainers)

	// The scoped rule list keeps its order, which OBS evaluates as
	// most specific match wins.
	assert.Equal(t, Flag{
		{Action: obs.FlagActionDisable},
		{Action: obs.FlagActionEnable, Repository: "rpm"},
		{Action: obs.FlagActionDisable, Repository: "rpm", Arch: "s390x"},
	}, project.Flags.Build)

	require.Len(t, project.Repositories, 2)

	rpm := project.Repositories[0]
	assert.Equal(t, "rpm", rpm.Name)
	assert.Equal(t, []string{"x86_64", "aarch64", "ppc64le", "s390x"}, rpm.Architectures)
	assert.Equal(t, []RepositoryPath{{Project: "SUSE:SLE-15-SP5:GA", Repository: "standard"}}, rpm.Paths)
	assert.Equal(t, []ReleaseTarget{{
		Project:    "isv:kubernetes:core:stable:v1.34",
		Repository: "rpm",
		Trigger:    obs.ReleaseTriggerManual,
	}}, rpm.ReleaseTargets)

	assert.Len(t, project.Repositories[1].Paths, 2)

	require.Len(t, project.Packages.Managed, 3)
	assert.Equal(t, PolicyReport, project.Packages.PolicyOrDefault())

	// A name only item declares the minimal package meta.
	assert.Equal(t, Package{Name: "kubelet"}, project.Packages.Managed[0])

	criTools := project.Packages.Managed[2]
	assert.Equal(t, "cri-tools", criTools.Name)
	assert.Equal(t, "CRI Tools", criTools.Title)
	assert.Equal(t, []string{"xmudrii"}, criTools.People.Maintainers)
	assert.Equal(t, Flag{{Action: obs.FlagActionEnable}}, criTools.Flags.Build)
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"top level":   "name: isv:kubernetes\ntypo: true\n",
		"nested":      "name: isv:kubernetes\ngroups:\n  typo: [a]\n",
		"repository":  "name: isv:kubernetes\nrepositories:\n  - name: rpm\n    typo: true\n",
		"packages":    "name: isv:kubernetes\npackages:\n  typo: true\n",
		"managed":     "name: isv:kubernetes\npackages:\n  managed:\n    - name: a\n      typo: 1\n",
		"flags block": "name: isv:kubernetes\nflags:\n  typo: disable\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := LoadFile(writeConfig(t, content))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not found")
		})
	}
}

func TestLoadRejectsInvalidFlags(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"unknown action":       "name: p\nflags:\n  build: nope\n",
		"unknown list action":  "name: p\nflags:\n  build: [nope]\n",
		"mapping with two key": "name: p\nflags:\n  build:\n    - {enable: {}, disable: {}}\n",
		"unknown scope field":  "name: p\nflags:\n  build:\n    - enable: {typo: rpm}\n",
		"duplicate repository": "name: p\nflags:\n  build:\n    - enable: {repository: rpm, repository: deb}\n",
		"duplicate arch":       "name: p\nflags:\n  build:\n    - disable: {arch: x86_64, arch: s390x}\n",
		"wrong kind":           "name: p\nflags:\n  build: {enable: {}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := LoadFile(writeConfig(t, content))
			require.Error(t, err)
		})
	}
}

func TestLoadFlagForms(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		content  string
		expected Flag
	}{
		"scalar shorthand": {
			content:  "name: p\nflags:\n  build: disable\n",
			expected: Flag{{Action: obs.FlagActionDisable}},
		},
		"list of scalars": {
			content:  "name: p\nflags:\n  build: [disable, enable]\n",
			expected: Flag{{Action: obs.FlagActionDisable}, {Action: obs.FlagActionEnable}},
		},
		"scoped by repository": {
			content:  "name: p\nflags:\n  build:\n    - enable: {repository: rpm}\n",
			expected: Flag{{Action: obs.FlagActionEnable, Repository: "rpm"}},
		},
		"scoped by arch": {
			content:  "name: p\nflags:\n  build:\n    - disable: {arch: s390x}\n",
			expected: Flag{{Action: obs.FlagActionDisable, Arch: "s390x"}},
		},
		"empty scope behaves like a scalar": {
			content:  "name: p\nflags:\n  build:\n    - enable:\n",
			expected: Flag{{Action: obs.FlagActionEnable}},
		},
		"empty list": {
			content:  "name: p\nflags:\n  build: []\n",
			expected: Flag{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			project, err := LoadFile(writeConfig(t, testCase.content))
			require.NoError(t, err)
			assert.Equal(t, testCase.expected, project.Flags.Build)
		})
	}
}

// TestLoadEveryFlagElement pins the complete set of modelled OBS flag
// elements: the four the OBS web UI exposes. Anything else is not expressible
// and is dropped from the project by the next apply, so the set is deliberate.
func TestLoadEveryFlagElement(t *testing.T) {
	t.Parallel()

	project, err := LoadFile(writeConfig(t, `name: p
flags:
  build: disable
  publish: disable
  useforbuild: disable
  debuginfo: enable
`))
	require.NoError(t, err)

	assert.Equal(t, Flags{
		Build:       Flag{{Action: obs.FlagActionDisable}},
		Publish:     Flag{{Action: obs.FlagActionDisable}},
		UseForBuild: Flag{{Action: obs.FlagActionDisable}},
		DebugInfo:   Flag{{Action: obs.FlagActionEnable}},
	}, project.Flags)

	assert.False(t, project.Flags.IsEmpty())
	assert.True(t, (&Flags{}).IsEmpty())
}

// TestLoadRejectsUnmodelledFlag pins that a flag outside the modelled set is a
// loud error rather than a silently ignored key.
func TestLoadRejectsUnmodelledFlag(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"binarydownload", "sourceaccess", "access", "lock"} {
		_, err := LoadFile(writeConfig(t, "name: p\nflags:\n  "+flag+": disable\n"))
		require.ErrorContains(t, err, flag)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	t.Run("missing path", func(t *testing.T) {
		t.Parallel()

		_, err := Load(filepath.Join("testdata", "does-not-exist"))
		require.Error(t, err)
	})

	t.Run("empty directory", func(t *testing.T) {
		t.Parallel()

		_, err := Load(t.TempDir())
		require.ErrorContains(t, err, "no YAML files found")
	})

	t.Run("empty file", func(t *testing.T) {
		t.Parallel()

		_, err := LoadFile(writeConfig(t, ""))
		require.ErrorContains(t, err, "is empty")
	})

	t.Run("multiple documents", func(t *testing.T) {
		t.Parallel()

		_, err := LoadFile(writeConfig(t, "name: a\n---\nname: b\n"))
		require.ErrorContains(t, err, "exactly one project")
	})
}

func TestLoadIgnoresNonYAMLFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "project.yaml"), "name: isv:kubernetes\n")
	writeFile(t, filepath.Join(dir, "other.yml"), "name: isv:kubernetes:core\n")
	writeFile(t, filepath.Join(dir, "README.md"), "not a config\n")
	writeFile(t, filepath.Join(dir, "OWNERS"), "not a config\n")

	config, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"isv:kubernetes:core", "isv:kubernetes"}, projectNames(config))
}

func TestProjectHierarchyHelpers(t *testing.T) {
	t.Parallel()

	project := &Project{Name: "isv:kubernetes:core:stable"}
	assert.Equal(t, 3, project.Depth())

	parent, ok := project.Parent()
	assert.True(t, ok)
	assert.Equal(t, "isv:kubernetes:core", parent)

	root := &Project{Name: "isv"}
	assert.Equal(t, 0, root.Depth())

	_, ok = root.Parent()
	assert.False(t, ok)
}

// writeConfig writes content to a temporary YAML file and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "project.yaml")
	writeFile(t, path, content)

	return path
}

// writeFile writes content to path.
func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// projectNames returns the names of all loaded projects in load order.
func projectNames(config *Config) []string {
	names := make([]string, 0, len(config.Projects))
	for _, project := range config.Projects {
		names = append(names, project.Name)
	}

	return names
}
