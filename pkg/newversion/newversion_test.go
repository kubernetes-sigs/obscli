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

package newversion

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/obscli/pkg/config"
)

func TestOptionsValidate(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		from, to string
		errorIs  string
	}{
		"valid":          {from: "v1.34", to: "v1.35"},
		"missing prefix": {from: "1.34", to: "v1.35", errorIs: `--from "1.34"`},
		"patch version":  {from: "v1.34", to: "v1.35.0", errorIs: `--to "v1.35.0"`},
		"not a version":  {from: "v1.34", to: "stable", errorIs: `--to "stable"`},
		"same version":   {from: "v1.34", to: "v1.34", errorIs: `both "v1.34"`},
		"empty":          {from: "", to: "v1.35", errorIs: `--from ""`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := Options{From: test.from, To: test.to}.Validate()

			if test.errorIs == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, test.errorIs)
		})
	}
}

func TestGenerate(t *testing.T) {
	t.Parallel()

	dir := configDir(t)

	files, err := Generate(Options{ConfigDir: dir, From: "v1.34", To: "v1.35"})
	require.NoError(t, err)

	require.Len(t, files, 2)

	// Only the files of the source version are copied, and each copy lands
	// next to its source.
	assert.Equal(t, filepath.Join(dir, "core", "stable", "v1.34.build.yaml"), files[0].Source)
	assert.Equal(t, filepath.Join(dir, "core", "stable", "v1.35.build.yaml"), files[0].Path)
	assert.Equal(t, filepath.Join(dir, "core", "stable", "v1.34.yaml"), files[1].Source)
	assert.Equal(t, filepath.Join(dir, "core", "stable", "v1.35.yaml"), files[1].Path)

	assert.Equal(t, `name: isv:kubernetes:core:stable:v1.35
title: Kubernetes 1.35
maintainers:
  - owner
repositories:
  - name: rpm
    architectures:
      - x86_64
    paths:
      - project: isv:kubernetes:core:stable:v1.35:build
        repository: rpm
`, string(files[1].Content))

	// Nothing is written until Write is called.
	assert.NoFileExists(t, files[1].Path)
}

// TestGenerateLeavesLongerVersionsAlone guards the version rewriting against
// the classic substring trap: v1.3 must not match inside v1.34.
func TestGenerateLeavesLongerVersionsAlone(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, "v1.3.yaml"),
		"name: isv:kubernetes:core:stable:v1.3\ntitle: Kubernetes 1.3, successor of 1.34\n")
	write(t, filepath.Join(dir, "v1.34.yaml"),
		"name: isv:kubernetes:core:stable:v1.34\ntitle: Kubernetes 1.34\n")

	files, err := Generate(Options{ConfigDir: dir, From: "v1.3", To: "v1.4"})
	require.NoError(t, err)

	require.Len(t, files, 1)
	assert.Equal(t, filepath.Join(dir, "v1.4.yaml"), files[0].Path)
	assert.Equal(t,
		"name: isv:kubernetes:core:stable:v1.4\ntitle: Kubernetes 1.4, successor of 1.34\n",
		string(files[0].Content),
	)
}

func TestGenerateKeepsTargetsBelowConfigDir(t *testing.T) {
	t.Parallel()

	configDir := filepath.Join(t.TempDir(), "release-v1.34", "config")
	write(t, filepath.Join(configDir, "v1.34.yaml"),
		"name: isv:kubernetes:core:stable:v1.34\n")

	files, err := Generate(Options{ConfigDir: configDir, From: "v1.34", To: "v1.35"})
	require.NoError(t, err)
	require.Len(t, files, 1)

	assert.Equal(t, filepath.Join(configDir, "v1.35.yaml"), files[0].Path)
}

func TestGenerateErrors(t *testing.T) {
	t.Parallel()

	t.Run("invalid options", func(t *testing.T) {
		t.Parallel()

		_, err := Generate(Options{ConfigDir: t.TempDir(), From: "v1.34", To: "1.35"})
		require.ErrorContains(t, err, "--to")
	})

	t.Run("unknown source version", func(t *testing.T) {
		t.Parallel()

		dir := configDir(t)

		_, err := Generate(Options{ConfigDir: dir, From: "v1.99", To: "v1.100"})
		require.ErrorContains(t, err, "no project of version v1.99")
	})

	t.Run("refuses to overwrite", func(t *testing.T) {
		t.Parallel()

		dir := configDir(t)
		write(t, filepath.Join(dir, "core", "stable", "v1.35.yaml"),
			"name: isv:kubernetes:core:stable:v1.35\n")

		_, err := Generate(Options{ConfigDir: dir, From: "v1.34", To: "v1.35"})
		require.ErrorContains(t, err, "already exists, refusing to overwrite it")
	})
}

func TestWriteRefusesTargetCreatedAfterGenerate(t *testing.T) {
	t.Parallel()

	dir := configDir(t)
	files, err := Generate(Options{ConfigDir: dir, From: "v1.34", To: "v1.35"})
	require.NoError(t, err)
	require.NotEmpty(t, files)

	require.NoError(t, os.WriteFile(files[0].Path, []byte("concurrent content\n"), 0o600))

	err = Write(files)
	require.ErrorContains(t, err, "already exists, refusing to overwrite it")

	content, err := os.ReadFile(files[0].Path)
	require.NoError(t, err)
	assert.Equal(t, "concurrent content\n", string(content))

	for _, file := range files[1:] {
		assert.NoFileExists(t, file.Path)
	}
}

func TestWriteRollsBackFilesCreatedBeforeFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := filepath.Join(dir, "first.yaml")
	existing := filepath.Join(dir, "existing.yaml")
	require.NoError(t, os.WriteFile(existing, []byte("keep\n"), 0o600))

	err := Write([]File{
		{Path: first, Content: []byte("first\n")},
		{Path: existing, Content: []byte("replacement\n")},
	})
	require.ErrorContains(t, err, "already exists, refusing to overwrite it")

	assert.NoFileExists(t, first)

	content, err := os.ReadFile(existing)
	require.NoError(t, err)
	assert.Equal(t, "keep\n", string(content))
}

// TestWriteProducesLoadableConfiguration is the guarantee that makes the
// command usable: the scaffolded files are a valid configuration of the new
// version, ready to be applied.
func TestWriteProducesLoadableConfiguration(t *testing.T) {
	t.Parallel()

	dir := configDir(t)

	files, err := Generate(Options{ConfigDir: dir, From: "v1.34", To: "v1.35"})
	require.NoError(t, err)
	require.NoError(t, Write(files))

	loaded, err := config.Load(dir)
	require.NoError(t, err)
	require.NoError(t, config.Validate(loaded, config.ValidateOptions{}))

	names := make([]string, 0, len(loaded.Projects))
	for _, project := range loaded.Projects {
		names = append(names, project.Name)
	}

	assert.Contains(t, names, "isv:kubernetes:core:stable:v1.35")
	assert.Contains(t, names, "isv:kubernetes:core:stable:v1.35:build")
	assert.Contains(t, names, "isv:kubernetes:core:stable:v1.34")
}

// configDir builds a small configuration tree holding one version and the
// projects above it.
func configDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	write(t, filepath.Join(dir, "kubernetes.yaml"),
		"name: isv:kubernetes\ntitle: Kubernetes\nmaintainers:\n  - owner\n")
	write(t, filepath.Join(dir, "core.yaml"),
		"name: isv:kubernetes:core\ntitle: Core\nmaintainers:\n  - owner\n")
	write(t, filepath.Join(dir, "core", "stable.yaml"),
		"name: isv:kubernetes:core:stable\ntitle: Stable\nmaintainers:\n  - owner\n")

	write(t, filepath.Join(dir, "core", "stable", "v1.34.yaml"),
		`name: isv:kubernetes:core:stable:v1.34
title: Kubernetes 1.34
maintainers:
  - owner
repositories:
  - name: rpm
    architectures:
      - x86_64
    paths:
      - project: isv:kubernetes:core:stable:v1.34:build
        repository: rpm
`)

	write(t, filepath.Join(dir, "core", "stable", "v1.34.build.yaml"),
		`name: isv:kubernetes:core:stable:v1.34:build
title: Kubernetes 1.34 builder
maintainers:
  - owner
repositories:
  - name: rpm
    architectures:
      - x86_64
    releaseTargets:
      - project: isv:kubernetes:core:stable:v1.34
        repository: rpm
        trigger: manual
`)

	return dir
}

// write creates a file and the directories leading to it.
func write(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
