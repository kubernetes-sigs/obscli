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
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const exportProjectXML = `<project name="isv:kubernetes"><title>Kubernetes</title><description></description><person userid="owner" role="maintainer"/></project>`

const exportProjectYAML = "name: isv:kubernetes\ntitle: Kubernetes\nmaintainers:\n  - owner\n"

func TestExportRejectsNonEmptyOutputBeforeOBS(t *testing.T) {
	for name, credentials := range map[string]bool{"without credentials": false, "with credentials": true} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(usernameEnv, "")
			t.Setenv(passwordEnv, "")

			if credentials {
				t.Setenv(usernameEnv, "owner")
				t.Setenv(passwordEnv, "not-a-real-password")
			}

			var requests atomic.Int64

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				http.Error(w, "unexpected OBS request", http.StatusInternalServerError)
			}))
			defer server.Close()

			outputDir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(outputDir, ".keep"), []byte("preserve me\n"), 0o600))

			var output bytes.Buffer

			cmd := rootCommand()
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs([]string{
				"export", "--project", "isv:kubernetes", "--output", outputDir, "--api-url", server.URL,
			})

			err := cmd.Execute()
			require.EqualError(t, err,
				fmt.Sprintf("output directory %q is not empty; choose a new or empty directory", outputDir))
			assert.Zero(t, requests.Load())
			assert.Empty(t, output.String())

			entries, err := os.ReadDir(outputDir)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, ".keep", entries[0].Name())
		})
	}
}

func TestExportOutputDestinations(t *testing.T) {
	t.Setenv(usernameEnv, "owner")
	t.Setenv(passwordEnv, "not-a-real-password")

	for _, destination := range []string{"stdout", "empty directory", "new directory"} {
		t.Run(destination, func(t *testing.T) {
			var requests atomic.Int64

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/source/isv:kubernetes/_meta", r.URL.Path)
				w.Header().Set("Content-Type", "application/xml")
				_, err := fmt.Fprint(w, exportProjectXML)
				assert.NoError(t, err)
			}))
			defer server.Close()

			args := []string{"export", "--project", "isv:kubernetes", "--api-url", server.URL}

			var outputDir string

			switch destination {
			case "empty directory":
				outputDir = t.TempDir()
			case "new directory":
				outputDir = filepath.Join(t.TempDir(), "new", "export")
			}

			if outputDir != "" {
				args = append(args, "--output", outputDir)
			}

			var output bytes.Buffer

			cmd := rootCommand()
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
			assert.Equal(t, int64(1), requests.Load())

			if outputDir == "" {
				assert.YAMLEq(t, exportProjectYAML, output.String())
			} else {
				assert.Equal(t, "kubernetes.yaml\n", output.String())

				content, err := os.ReadFile(filepath.Join(outputDir, "kubernetes.yaml"))
				require.NoError(t, err)
				assert.YAMLEq(t, exportProjectYAML, string(content))
			}
		})
	}
}

func TestExportRechecksOutputAfterReadingOBS(t *testing.T) {
	t.Setenv(usernameEnv, "owner")
	t.Setenv(passwordEnv, "not-a-real-password")

	outputDir := t.TempDir()
	destination := filepath.Join(outputDir, "kubernetes.yaml")

	var requests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)

		if err := os.WriteFile(destination, []byte("preserve me\n"), 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		w.Header().Set("Content-Type", "application/xml")
		_, err := fmt.Fprint(w, exportProjectXML)
		assert.NoError(t, err)
	}))
	defer server.Close()

	var output bytes.Buffer

	cmd := rootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{
		"export", "--project", "isv:kubernetes", "--output", outputDir, "--api-url", server.URL,
	})
	require.ErrorContains(t, cmd.Execute(), "is not empty; choose a new or empty directory")
	assert.Equal(t, int64(1), requests.Load())
	assert.Empty(t, output.String())

	content, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, "preserve me\n", string(content))
}

func TestExportRoundTripsSpecialPackageNames(t *testing.T) {
	t.Setenv(usernameEnv, "owner")
	t.Setenv(passwordEnv, "not-a-real-password")

	reads := []obsResponse{
		{method: http.MethodGet, path: "/source/isv:kubernetes/_meta", body: exportProjectXML},
		{
			method: http.MethodGet, path: "/source/isv:kubernetes",
			body: `<directory><entry name="_patchinfo"/><entry name="_product:example"/></directory>`,
		},
		{
			method: http.MethodGet, path: "/source/isv:kubernetes/_patchinfo/_meta",
			body: `<package name="_patchinfo" project="isv:kubernetes"><title></title><description></description></package>`,
		},
		{
			method: http.MethodGet, path: "/source/isv:kubernetes/_product:example/_meta",
			body: `<package name="_product:example" project="isv:kubernetes"><title></title><description></description></package>`,
		},
	}
	responses := append([]obsResponse{}, reads...)
	responses = append(responses, reads...)
	responses = append(responses, obsResponse{
		method: http.MethodGet, path: "/search/project/id", body: `<collection/>`,
	})
	server := newTestOBS(t, responses)

	document, err := executeTestCommand(t,
		"export", "--project", "isv:kubernetes", "--with-packages", "--api-url", server.URL,
	)
	require.NoError(t, err)
	assert.Contains(t, document, "name: _patchinfo")
	assert.Contains(t, document, "name: _product:example")
	path := writeTestConfig(t, t.TempDir(), "project.yaml", document)
	_, err = executeTestCommand(t, "validate", "--config", path, "--api-url", server.URL)
	require.NoError(t, err)
	output, err := executeTestCommand(t, "apply", "--config", path, "--api-url", server.URL, "--confirm")
	require.NoError(t, err)
	assert.Contains(t, output, "Projects: 0 created, 0 updated, 0 deleted")
	assert.Contains(t, output, "Packages: 0 created, 0 updated, 0 deleted")
	assert.Contains(t, output, "Everything is up to date.")
}
