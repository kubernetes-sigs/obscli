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
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"sigs.k8s.io/obscli/pkg/config"
	"sigs.k8s.io/obscli/pkg/sync"
)

// applyOptions are the flags of the apply command.
type applyOptions struct {
	configPath          string
	confirm             bool
	allowDeletions      bool
	requiredMaintainers []string
}

// applyCommand reconciles the live OBS state with the configuration.
func applyCommand(global *globalOptions) *cobra.Command {
	opts := &applyOptions{}

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Reconcile the OBS state with the configuration",
		Long: "Apply loads the desired state, compares it to the live OBS state and logs every\n" +
			"change it would make. It is a dry run unless --confirm is given, and it never\n" +
			"deletes anything unless --confirm and --allow-deletions are both given.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runApply(cmd, global, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.configPath, "config", "", "path to a configuration file or directory")
	flags.BoolVar(&opts.confirm, "confirm", false, "carry the changes out instead of only reporting them")
	flags.BoolVar(&opts.allowDeletions, "allow-deletions", false, "permit deletions")
	flags.StringSliceVar(&opts.requiredMaintainers, "required-maintainers", nil,
		"users that must be a maintainer of every declared project")

	if err := cmd.MarkFlagRequired("config"); err != nil {
		panic(err)
	}

	return cmd
}

// runApply loads, validates and reconciles the desired state.
func runApply(cmd *cobra.Command, global *globalOptions, opts *applyOptions) error {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}

	if err := config.Validate(cfg, config.ValidateOptions{
		RequiredMaintainers: opts.requiredMaintainers,
	}); err != nil {
		return fmt.Errorf("validating configuration:\n%w", err)
	}

	client, err := newClient(global, !opts.confirm)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	logger := sync.NewLogger(out)

	if err := logger.Infof("Reconciling %d project(s) against %s", len(cfg.Projects), global.apiURL); err != nil {
		return err
	}

	if !opts.confirm {
		if err := logger.Infof("Running in dry run mode, no change is written."); err != nil {
			return err
		}
	}

	if err := logger.Infof(""); err != nil {
		return err
	}

	summary, err := sync.Reconcile(cmd.Context(), client, cfg, logger, sync.Options{
		Confirm:        opts.confirm,
		AllowDeletions: opts.allowDeletions,
	})

	// The summary is printed even when the reconciliation failed halfway
	// through, because it states what was already changed.
	summaryErr := printSummary(logger, summary, opts.confirm, err)

	return errors.Join(explainError(err), summaryErr)
}

// printSummary states what happened, or what would happen in a dry run. A
// failed run gets no verdict: the summary only covers the work done before the
// failure, and the state of everything after it is unknown.
func printSummary(logger *sync.Logger, summary sync.Summary, confirm bool, runErr error) error {
	if err := logger.Infof(""); err != nil {
		return err
	}

	if err := logger.Infof(
		"Projects: %d created, %d updated, %d deleted",
		summary.ProjectsCreated, summary.ProjectsUpdated, summary.ProjectsDeleted,
	); err != nil {
		return err
	}

	if err := logger.Infof(
		"Packages: %d created, %d updated, %d deleted",
		summary.PackagesCreated, summary.PackagesUpdated, summary.PackagesDeleted,
	); err != nil {
		return err
	}

	if summary.Reported > 0 {
		if err := logger.Infof("Reported but left alone: %d", summary.Reported); err != nil {
			return err
		}
	}

	switch {
	case runErr != nil:
		return logger.Infof("The run stopped early, the counts above only cover what was reached.")
	case !summary.Changed():
		return logger.Infof("Everything is up to date.")
	case confirm:
		return logger.Infof("Changes applied.")
	default:
		return logger.Infof("This was a dry run, run again with --confirm to apply the changes.")
	}
}
