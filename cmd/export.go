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
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"sigs.k8s.io/obscli/pkg/config"
	"sigs.k8s.io/obscli/pkg/export"
)

// exportOptions are the flags of the export command.
type exportOptions struct {
	project      string
	recursive    bool
	output       string
	withPackages bool
}

// exportCommand bootstraps configuration from live OBS state.
func exportCommand(global *globalOptions) *cobra.Command {
	opts := &exportOptions{}

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Bootstrap configuration from live OBS state",
		Long: "Export reads a project, and optionally its subprojects, from OBS and renders it\n" +
			"as obscli configuration. Applying a freshly exported configuration against the\n" +
			"same OBS state produces no change.\n\n" +
			"Export is a one-time bootstrap. The output directory must be new or empty;\n" +
			"export does not refresh an existing configuration tree.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runExport(cmd, global, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.project, "project", "", "the OBS project to export")
	flags.BoolVar(&opts.recursive, "recursive", false, "include every subproject")
	flags.StringVar(&opts.output, "output", "", "new or empty directory to write to, defaults to standard output")
	flags.BoolVar(&opts.withPackages, "with-packages", false, "include the packages of every project")

	if err := cmd.MarkFlagRequired("project"); err != nil {
		panic(err)
	}

	return cmd
}

// runExport reads the live state and renders it.
func runExport(cmd *cobra.Command, global *globalOptions, opts *exportOptions) error {
	if opts.output != "" {
		if err := export.CheckOutputDir(opts.output); err != nil {
			return err
		}
	}

	// Export never writes to OBS, so the client is always a dry run one.
	client, err := newClient(global, true)
	if err != nil {
		return err
	}

	projects, err := export.Export(cmd.Context(), client, export.Options{
		Project:      opts.project,
		Recursive:    opts.recursive,
		WithPackages: opts.withPackages,
	})
	if err != nil {
		return explainError(err)
	}

	if opts.output == "" {
		return printProjects(cmd, projects)
	}

	layout, err := export.Write(projects, opts.project, opts.output)
	if err != nil {
		return err
	}

	paths := make([]string, 0, len(layout))
	for _, path := range layout {
		paths = append(paths, path)
	}

	sort.Strings(paths)

	out := cmd.OutOrStdout()
	for _, path := range paths {
		if _, err := fmt.Fprintln(out, path); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	}

	return nil
}

// printProjects renders every project to standard output as a YAML stream.
func printProjects(cmd *cobra.Command, projects []*config.Project) error {
	out := cmd.OutOrStdout()

	for i, project := range projects {
		rendered, err := export.Marshal(project)
		if err != nil {
			return err
		}

		if i > 0 {
			if _, err := fmt.Fprintln(out, "---"); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
		}

		if _, err := fmt.Fprint(out, string(rendered)); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	}

	return nil
}
