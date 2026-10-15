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
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"sigs.k8s.io/obscli/pkg/config"
)

// yamlIndent is the indentation of the generated files. Two spaces is what
// hand written Kubernetes configuration uses.
const yamlIndent = 2

// Layout maps every exported project to the file it is written to, following
// the repository convention: a project becomes a directory only when it has a
// grandchild, so that a project and its single builder project end up next to
// each other instead of in a directory of their own.
//
//	isv:kubernetes                          kubernetes.yaml
//	isv:kubernetes:core                     core.yaml
//	isv:kubernetes:core:stable              core/stable.yaml
//	isv:kubernetes:core:stable:v1.34        core/stable/v1.34.yaml
//	isv:kubernetes:core:stable:v1.34:build  core/stable/v1.34.build.yaml
//
// The layout is a convention only. obscli loads whatever YAML it finds and
// takes the project identity from the name field, never from the path.
func Layout(root string, projects []*config.Project) (map[string]string, error) {
	if !config.ValidProjectName(root) {
		return nil, fmt.Errorf("%q is not a valid OBS project name", root)
	}

	names := make([]string, 0, len(projects))
	seenNames := make(map[string]struct{}, len(projects))

	for _, project := range projects {
		if project == nil {
			return nil, errors.New("exported project must not be nil")
		}

		if !config.ValidProjectName(project.Name) {
			return nil, fmt.Errorf("%q is not a valid OBS project name", project.Name)
		}

		if project.Name != root && !strings.HasPrefix(project.Name, root+":") {
			return nil, fmt.Errorf("project %q is not %q or one of its subprojects", project.Name, root)
		}

		if _, exists := seenNames[project.Name]; exists {
			return nil, fmt.Errorf("project %q is exported more than once", project.Name)
		}

		seenNames[project.Name] = struct{}{}
		names = append(names, project.Name)
	}

	sort.Strings(names)

	rootSegment := root
	if _, after, ok := strings.CutLast(root, ":"); ok {
		rootSegment = after
	}

	layout := make(map[string]string, len(names))
	pathOwners := make(map[string]string, len(names))
	directoryUsers := make(map[string]string, len(names))

	for _, name := range names {
		var path string

		if name == root {
			path = rootSegment + ".yaml"
		} else {
			path = projectPath(strings.TrimPrefix(name, root+":"), name, names)
		}

		path = filepath.Clean(path)
		if !filepath.IsLocal(path) {
			return nil, fmt.Errorf("project %q maps outside the export directory: %s", name, path)
		}

		// Reject collisions portably, including the case-insensitive file
		// systems commonly used on macOS and Windows.
		pathKey := strings.ToLower(path)
		if previous, exists := pathOwners[pathKey]; exists {
			return nil, fmt.Errorf(
				"projects %q and %q both map to %s",
				previous, name, path,
			)
		}

		if descendant, exists := directoryUsers[pathKey]; exists {
			return nil, fmt.Errorf(
				"project %q maps to file %s, but project %q needs that path to be a directory",
				name, path, descendant,
			)
		}

		for directory := filepath.Dir(pathKey); directory != "."; directory = filepath.Dir(directory) {
			if previous, exists := pathOwners[directory]; exists {
				return nil, fmt.Errorf(
					"project %q maps below %s, but project %q maps that path to a file",
					name, directory, previous,
				)
			}

			if _, exists := directoryUsers[directory]; !exists {
				directoryUsers[directory] = name
			}
		}

		pathOwners[pathKey] = name
		layout[name] = path
	}

	return layout, nil
}

// projectPath builds the file path of a project from the part of its name
// below the root.
func projectPath(relative, name string, names []string) string {
	segments := strings.Split(relative, ":")

	// Every ancestor below the root that has a grandchild becomes a directory.
	// The remaining segments are joined with a dot into the file name.
	prefix := name[:len(name)-len(relative)]

	var (
		directories []string
		flattened   []string
	)

	for i, segment := range segments {
		// The last segment names the file, never a directory: a project that
		// has a grandchild gets both a file and a directory next to it.
		isLast := i == len(segments)-1

		if !isLast && len(flattened) == 0 && hasGrandchild(prefix+strings.Join(segments[:i+1], ":"), names) {
			directories = append(directories, segment)

			continue
		}

		flattened = append(flattened, segment)
	}

	return filepath.Join(append(directories, strings.Join(flattened, ".")+".yaml")...)
}

// hasGrandchild reports whether any exported project lives at least two levels
// below the given one.
func hasGrandchild(name string, names []string) bool {
	prefix := name + ":"

	for _, candidate := range names {
		if !strings.HasPrefix(candidate, prefix) {
			continue
		}

		if strings.Contains(candidate[len(prefix):], ":") {
			return true
		}
	}

	return false
}

// Marshal renders a project as a YAML document. Every empty field is omitted
// and a flag carrying a single unscoped rule is rendered as a plain string, so
// that the result reads as if it had been written by hand.
func Marshal(project *config.Project) ([]byte, error) {
	buffer := &bytes.Buffer{}

	encoder := yaml.NewEncoder(buffer)
	encoder.SetIndent(yamlIndent)

	if err := encoder.Encode(project); err != nil {
		return nil, fmt.Errorf("rendering project %s: %w", project.Name, err)
	}

	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("rendering project %s: %w", project.Name, err)
	}

	return buffer.Bytes(), nil
}

// CheckOutputDir requires an output directory that does not exist or is empty.
// It never creates or changes the directory.
func CheckOutputDir(outputDir string) error {
	entries, err := os.ReadDir(outputDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("reading output directory %q: %w", outputDir, err)
	}

	if len(entries) > 0 {
		return fmt.Errorf("output directory %q is not empty; choose a new or empty directory", outputDir)
	}

	return nil
}

// Write renders every project into a new or empty output directory, creating
// the directories of the layout as needed. The returned layout is the exact
// plan that was written.
func Write(projects []*config.Project, root, outputDir string) (map[string]string, error) {
	layout, err := Layout(root, projects)
	if err != nil {
		return nil, err
	}

	if err := CheckOutputDir(outputDir); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating export directory %s: %w", outputDir, err)
	}

	outputRoot, err := os.OpenRoot(outputDir)
	if err != nil {
		return nil, fmt.Errorf("opening export directory %s: %w", outputDir, err)
	}

	for _, project := range projects {
		rendered, err := Marshal(project)
		if err != nil {
			_ = outputRoot.Close()

			return nil, err
		}

		path := layout[project.Name]

		if err := outputRoot.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			_ = outputRoot.Close()

			return nil, fmt.Errorf("creating directory of %s: %w", path, err)
		}

		if err := writeFile(outputRoot, path, rendered); err != nil {
			_ = outputRoot.Close()

			return nil, fmt.Errorf("writing %s: %w", path, err)
		}
	}

	if err := outputRoot.Close(); err != nil {
		return nil, fmt.Errorf("closing export directory %s: %w", outputDir, err)
	}

	return layout, nil
}

// writeFile atomically replaces path without following a final-component
// symlink. os.Root also prevents intermediate symlinks from escaping the
// output directory.
func writeFile(root *os.Root, path string, content []byte) error {
	tempPath := filepath.Join(filepath.Dir(path), ".obscli-"+rand.Text()+".tmp")

	file, err := root.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}

	removeTemp := true
	defer func() {
		if removeTemp {
			_ = root.Remove(tempPath)
		}
	}()

	if _, err := file.Write(content); err != nil {
		_ = file.Close()

		return err
	}

	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()

		return err
	}

	if err := file.Close(); err != nil {
		return err
	}

	if err := root.Rename(tempPath, path); err != nil {
		return err
	}

	removeTemp = false

	return nil
}
