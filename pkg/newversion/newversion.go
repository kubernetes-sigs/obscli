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

// Package newversion scaffolds the configuration of a new Kubernetes minor
// version by copying the files of an existing one.
package newversion

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/obscli/pkg/config"
)

// versionRegex matches a Kubernetes minor version such as v1.34.
var versionRegex = regexp.MustCompile(`^v\d+\.\d+$`)

// Options configures the scaffolding.
type Options struct {
	// ConfigDir is the directory holding the configuration.
	ConfigDir string

	// From is the existing version to copy, such as v1.34.
	From string

	// To is the new version to create, such as v1.35.
	To string
}

// File is a file the scaffolding creates.
type File struct {
	// Source is the file it was copied from.
	Source string

	// Path is the file that is created.
	Path string

	// Content is the rewritten content.
	Content []byte
}

// Validate rejects options that cannot produce a sensible result.
func (o Options) Validate() error {
	if !versionRegex.MatchString(o.From) {
		return fmt.Errorf("--from %q is not a Kubernetes minor version such as v1.34", o.From)
	}

	if !versionRegex.MatchString(o.To) {
		return fmt.Errorf("--to %q is not a Kubernetes minor version such as v1.34", o.To)
	}

	if o.From == o.To {
		return fmt.Errorf("--from and --to are both %q", o.From)
	}

	return nil
}

// Generate finds every configuration file declaring a project of the source
// version and returns the files of the new version. Nothing is written to
// disk, so that the caller can report the result before creating anything.
func Generate(opts Options) ([]File, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	cfg, err := config.Load(opts.ConfigDir)
	if err != nil {
		return nil, err
	}

	// A project belongs to the source version when one of its name segments is
	// exactly that version, so that v1.3 never matches v1.34.
	sources := make(map[string]struct{})

	for _, project := range cfg.Projects {
		if hasVersionSegment(project.Name, opts.From) {
			sources[project.SourcePath()] = struct{}{}
		}
	}

	if len(sources) == 0 {
		return nil, fmt.Errorf("no project of version %s found in %s", opts.From, opts.ConfigDir)
	}

	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}

	sort.Strings(paths)

	files := make([]File, 0, len(paths))

	for _, path := range paths {
		file, err := generateFile(path, opts)
		if err != nil {
			return nil, err
		}

		files = append(files, file)
	}

	return files, nil
}

// generateFile rewrites a single configuration file.
func generateFile(path string, opts Options) (File, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("reading %s: %w", path, err)
	}

	relative, err := filepath.Rel(opts.ConfigDir, path)
	if err != nil {
		return File{}, fmt.Errorf("locating %s below %s: %w", path, opts.ConfigDir, err)
	}

	if !filepath.IsLocal(relative) {
		return File{}, fmt.Errorf("%s is outside the configuration directory %s", path, opts.ConfigDir)
	}

	targetRelative := rewritePath(relative, opts.From, opts.To)
	if !filepath.IsLocal(targetRelative) {
		return File{}, fmt.Errorf("rewriting %s produced a path outside %s", path, opts.ConfigDir)
	}

	target := filepath.Join(opts.ConfigDir, targetRelative)

	switch _, err := os.Stat(target); {
	case err == nil:
		return File{}, fmt.Errorf("%s already exists, refusing to overwrite it", target)
	case !os.IsNotExist(err):
		return File{}, fmt.Errorf("checking %s: %w", target, err)
	}

	return File{
		Source:  path,
		Path:    target,
		Content: []byte(replaceVersion(string(content), opts.From, opts.To)),
	}, nil
}

// replaceVersion replaces every occurrence of a version, with or without the
// leading v, that is not part of a longer version. Titles and descriptions
// spell the version without the v, so both forms have to be rewritten, while
// rewriting v1.3 has to leave v1.34 alone.
func replaceVersion(content, from, to string) string {
	pattern := regexp.MustCompile(`\b(v?)` + regexp.QuoteMeta(strings.TrimPrefix(from, "v")) + `\b`)

	return pattern.ReplaceAllString(content, "${1}"+strings.TrimPrefix(to, "v"))
}

// Write creates the generated files on disk.
func Write(files []File) error {
	created := make([]string, 0, len(files))

	for _, file := range files {
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			return errors.Join(
				fmt.Errorf("creating directory of %s: %w", file.Path, err),
				rollback(created),
			)
		}

		if err := writeExclusive(file); err != nil {
			return errors.Join(err, rollback(created))
		}

		created = append(created, file.Path)
	}

	return nil
}

// writeExclusive creates one generated file without replacing an entry that
// appeared after Generate checked the target.
func writeExclusive(generated File) error {
	file, err := os.OpenFile(generated.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists, refusing to overwrite it", generated.Path)
		}

		return fmt.Errorf("creating %s: %w", generated.Path, err)
	}

	remove := true
	defer func() {
		if remove {
			_ = os.Remove(generated.Path)
		}
	}()

	if _, err := file.Write(generated.Content); err != nil {
		_ = file.Close()

		return fmt.Errorf("writing %s: %w", generated.Path, err)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", generated.Path, err)
	}

	remove = false

	return nil
}

// rollback removes files created by the current Write call.
func rollback(paths []string) error {
	var errs []error

	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("rolling back %s: %w", path, err))
		}
	}

	return errors.Join(errs...)
}

// hasVersionSegment reports whether one of the colon separated segments of a
// project name is exactly the given version.
func hasVersionSegment(projectName, version string) bool {
	return slices.Contains(strings.Split(projectName, ":"), version)
}

// rewritePath replaces the version in a relative file name and its directories,
// so that a copy lands next to its source without changing ConfigDir itself.
func rewritePath(path, from, to string) string {
	directory, name := filepath.Split(path)
	if directory == "" {
		return replaceVersion(name, from, to)
	}

	return filepath.Join(
		replaceVersion(filepath.Clean(directory), from, to),
		replaceVersion(name, from, to),
	)
}
