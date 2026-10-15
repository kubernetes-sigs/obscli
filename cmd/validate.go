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

	"sigs.k8s.io/obscli/pkg/config"
)

// validateCommand checks the configuration without touching OBS.
func validateCommand() *cobra.Command {
	var (
		configPath          string
		requiredMaintainers []string
	)

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check the configuration offline",
		Long: "Validate loads the configuration and reports every problem it finds. It needs\n" +
			"neither network access nor credentials, which makes it the check to run on a\n" +
			"pull request.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}

			if err := config.Validate(cfg, config.ValidateOptions{
				RequiredMaintainers: requiredMaintainers,
			}); err != nil {
				return fmt.Errorf("validating configuration:\n%w", err)
			}

			if _, err := fmt.Fprintf(
				cmd.OutOrStdout(),
				"The configuration is valid: %d project(s) declared.\n",
				len(cfg.Projects),
			); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}

			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&configPath, "config", "", "path to a configuration file or directory")
	flags.StringSliceVar(&requiredMaintainers, "required-maintainers", nil,
		"users that must be a maintainer of every declared project")

	if err := cmd.MarkFlagRequired("config"); err != nil {
		panic(err)
	}

	return cmd
}
