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
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/release-sdk/obs"
)

func TestCommandsRequireFlags(t *testing.T) {
	for _, command := range []string{"apply", "validate", "export", "new-version"} {
		t.Run(command, func(t *testing.T) {
			output, err := executeTestCommand(t, command)
			require.ErrorContains(t, err, "required flag")
			assert.Empty(t, output)
		})
	}
}

func TestCommandsRequireCredentials(t *testing.T) {
	configPath := writeTestConfig(t, t.TempDir(), "project.yaml", exportProjectYAML)

	for _, command := range []string{"apply", "export"} {
		for _, missing := range []string{usernameEnv, passwordEnv} {
			t.Run(command+"/"+missing, func(t *testing.T) {
				t.Setenv(usernameEnv, "owner")
				t.Setenv(passwordEnv, "not-a-real-password")
				t.Setenv(missing, "")
				server := newTestOBS(t, nil)

				args := []string{command, "--api-url", server.URL}
				if command == "apply" {
					args = append(args, "--config", configPath)
				} else {
					args = append(args, "--project", "isv:kubernetes")
				}

				output, err := executeTestCommand(t, args...)
				require.ErrorContains(t, err, "OBS credentials are missing")
				assert.Contains(t, err.Error(), usernameEnv)
				assert.Contains(t, err.Error(), passwordEnv)
				assert.Empty(t, output)
			})
		}
	}
}

func TestCommandsExplainAPIErrors(t *testing.T) {
	t.Setenv(usernameEnv, "owner")
	t.Setenv(passwordEnv, "not-a-real-password")
	configPath := writeTestConfig(t, t.TempDir(), "project.yaml", exportProjectYAML)

	for _, command := range []string{"apply", "export"} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
			t.Run(fmt.Sprintf("%s/%d", command, status), func(t *testing.T) {
				server := newTestOBS(t, []obsResponse{{
					method: http.MethodGet, path: "/source/isv:kubernetes/_meta",
					status: status, body: `<status code="denied"><summary>Request denied</summary></status>`,
				}})

				args := []string{command, "--api-url", server.URL}
				if command == "apply" {
					args = append(args, "--config", configPath, "--confirm")
				} else {
					args = append(args, "--project", "isv:kubernetes")
				}

				output, err := executeTestCommand(t, args...)
				require.Error(t, err)
				assert.Contains(t, err.Error(), fmt.Sprintf("HTTP %d", status))

				if status == http.StatusUnauthorized {
					assert.Contains(t, err.Error(), "check the OBS_USERNAME and OBS_PASSWORD")
				} else {
					assert.NotContains(t, err.Error(), "check the OBS_USERNAME")
				}

				assert.NotContains(t, output+err.Error(), "not-a-real-password")
				assert.NotContains(t, output, "Changes applied.")
				assert.NotContains(t, output, "Everything is up to date.")

				if command == "apply" {
					assert.Contains(t, output, "The run stopped early")
				} else {
					assert.Empty(t, output)
				}
			})
		}
	}
}

func TestValidateIsOffline(t *testing.T) {
	t.Setenv(usernameEnv, "")
	t.Setenv(passwordEnv, "")

	for name, test := range map[string]struct {
		document string
		required string
		wantErr  string
	}{
		"valid": {document: exportProjectYAML, required: "owner"},
		"special packages": {
			document: exportProjectYAML + `packages:
  managed:
    - name: _product
    - name: _pattern
    - name: _project
    - name: _patchinfo
    - name: _product:example
    - name: _patchinfo:example
`,
		},
		"unknown field": {
			document: exportProjectYAML + "typo: true\n",
			wantErr:  "field typo not found",
		},
		"invalid name": {
			document: "name: isv:../invalid\nmaintainers: [owner]\n",
			wantErr:  "not a valid OBS project name",
		},
		"missing owner": {
			document: "name: isv:kubernetes\n",
			wantErr:  "has no maintainer",
		},
		"required maintainer": {
			document: exportProjectYAML, required: "another-user",
			wantErr: `must have "another-user" as a maintainer`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := newTestOBS(t, nil)
			path := writeTestConfig(t, t.TempDir(), "project.yaml", test.document)

			args := []string{"validate", "--config", path, "--api-url", server.URL}
			if test.required != "" {
				args = append(args, "--required-maintainers", test.required)
			}

			output, err := executeTestCommand(t, args...)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				assert.Empty(t, output)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "The configuration is valid: 1 project(s) declared.\n", output)
			}
		})
	}
}

func TestNewVersionIsOffline(t *testing.T) {
	t.Setenv(usernameEnv, "")
	t.Setenv(passwordEnv, "")
	server := newTestOBS(t, nil)
	dir := t.TempDir()
	document := "name: isv:kubernetes:v1.34\ntitle: Kubernetes 1.34\nmaintainers: [owner]\n"
	source := writeTestConfig(t, dir, "v1.34.yaml", document)
	target := filepath.Join(dir, "v1.35.yaml")
	args := []string{
		"new-version", "--config", dir, "--from", "v1.34", "--to", "v1.35", "--api-url", server.URL,
	}

	output, err := executeTestCommand(t, args...)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("+ %s (from %s)\n\nCreated 1 file(s) for v1.35.\n", target, source), output)
	content, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, strings.ReplaceAll(document, "1.34", "1.35"), string(content))

	original, err := os.ReadFile(source)
	require.NoError(t, err)
	assert.Equal(t, document, string(original))

	output, err = executeTestCommand(t, args...)
	require.Error(t, err)
	assert.Empty(t, output)

	unchanged, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, content, unchanged)
}

func executeTestCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var output bytes.Buffer

	command := rootCommand()
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	err := command.ExecuteContext(t.Context())

	return output.String(), err
}

func writeTestConfig(t *testing.T, dir, name, document string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(document), 0o600))

	return path
}

type obsResponse struct {
	method string
	path   string
	status int
	body   string
	check  func(*http.Request)
}

// newTestOBS rejects unexpected requests and checks that every expected request
// was made. A nil response list asserts that a command stays offline.
func newTestOBS(t *testing.T, responses []obsResponse) *httptest.Server {
	t.Helper()

	var mu sync.Mutex

	next := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if next >= len(responses) {
			t.Errorf("unexpected OBS request: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected request", http.StatusInternalServerError)

			return
		}

		response := responses[next]
		next++

		assert.Equal(t, response.method, r.Method)
		assert.Equal(t, response.path, r.URL.Path)
		username, password, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "owner", username)
		assert.Equal(t, "not-a-real-password", password)

		if response.check != nil {
			response.check(r)
		}

		w.Header().Set("Content-Type", "application/xml")

		status := response.status
		if status == 0 {
			status = http.StatusOK
		}

		w.WriteHeader(status)
		_, err := fmt.Fprint(w, response.body)
		assert.NoError(t, err)
	}))

	t.Cleanup(func() {
		server.Close()
		mu.Lock()
		defer mu.Unlock()

		assert.Equal(t, len(responses), next, "not all expected OBS requests were made")
	})

	return server
}

func checkProjectBody(t *testing.T, want *obs.Project) func(*http.Request) {
	t.Helper()

	expected := *want
	expected.XMLName = xml.Name{Local: "project"}

	return func(r *http.Request) {
		var got obs.Project
		if assert.NoError(t, xml.NewDecoder(r.Body).Decode(&got)) {
			assert.Equal(t, expected, got)
		}
	}
}

func checkPackageBody(t *testing.T, want *obs.Package) func(*http.Request) {
	t.Helper()

	expected := *want
	expected.XMLName = xml.Name{Local: "package"}

	return func(r *http.Request) {
		var got obs.Package
		if assert.NoError(t, xml.NewDecoder(r.Body).Decode(&got)) {
			assert.Equal(t, expected, got)
		}
	}
}
