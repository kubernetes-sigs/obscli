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

	"github.com/spf13/cobra"

	"sigs.k8s.io/obscli/pkg/newversion"
)

// newVersionCommand scaffolds the configuration of a new Kubernetes minor
// version.
func newVersionCommand() *cobra.Command {
	opts := newversion.Options{}

	cmd := &cobra.Command{
		Use:   "new-version",
		Short: "Scaffold the configuration of a new Kubernetes minor version",
		Long: "New-version copies every configuration file of an existing Kubernetes minor\n" +
			"version and rewrites the version in the file names and in the file contents.\n" +
			"It is purely local and never talks to OBS. The result is reviewed and applied\n" +
			"like any other configuration change.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			files, err := newversion.Generate(opts)
			if err != nil {
				return err
			}

			if err := newversion.Write(files); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			for _, file := range files {
				if _, err := fmt.Fprintf(out, "+ %s (from %s)\n", file.Path, file.Source); err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
			}

			if _, err := fmt.Fprintf(out, "\nCreated %d file(s) for %s.\n", len(files), opts.To); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}

			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.ConfigDir, "config", "", "path to the configuration directory")
	flags.StringVar(&opts.From, "from", "", "the existing version to copy, such as v1.34")
	flags.StringVar(&opts.To, "to", "", "the new version to create, such as v1.35")

	for _, name := range []string{"config", "from", "to"} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(err)
		}
	}

	return cmd
}
