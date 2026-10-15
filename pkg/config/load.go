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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads the desired state from path, which is either a single YAML file
// or a directory that is searched recursively for *.yaml and *.yml files.
//
// Loading is strict: unknown fields are rejected, so that a typo in a key
// never silently drops configuration.
func Load(path string) (*Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("accessing config path %s: %w", path, err)
	}

	var files []string

	if info.IsDir() {
		files, err = findConfigFiles(path)
		if err != nil {
			return nil, err
		}

		if len(files) == 0 {
			return nil, fmt.Errorf("no YAML files found in %s", path)
		}
	} else {
		files = []string{path}
	}

	config := &Config{Projects: make([]*Project, 0, len(files))}

	for _, file := range files {
		project, err := LoadFile(file)
		if err != nil {
			return nil, err
		}

		config.Projects = append(config.Projects, project)
	}

	return config, nil
}

// LoadFile reads a single project from a YAML file.
func LoadFile(path string) (*Project, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening config file %s: %w", path, err)
	}
	defer file.Close()

	project := &Project{}

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)

	if err := decoder.Decode(project); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("config file %s is empty", path)
		}

		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	// A YAML file must hold exactly one project, otherwise the additional
	// documents would be dropped without a trace.
	if err := decoder.Decode(&yaml.Node{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config file %s must contain exactly one project", path)
	}

	project.sourcePath = path

	return project, nil
}

// findConfigFiles collects every YAML file below root in a stable order, so
// that error messages and logs do not depend on the file system.
func findConfigFiles(root string) ([]string, error) {
	var files []string

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		if isYAMLFile(path) {
			files = append(files, path)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("searching config files in %s: %w", root, err)
	}

	sort.Strings(files)

	return files, nil
}

// isYAMLFile reports whether path carries a YAML file extension.
func isYAMLFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}
