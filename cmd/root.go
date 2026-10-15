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

// Package cmd wires the obscli command line interface.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"sigs.k8s.io/release-sdk/obs"
)

// Environment variables carrying the OBS credentials. They fit the secret
// mounting of a CI job, which never passes credentials on the command line.
const (
	usernameEnv = "OBS_USERNAME"
	passwordEnv = "OBS_PASSWORD"
)

// globalOptions are the flags every command shares.
type globalOptions struct {
	apiURL string
}

// Execute runs the obscli command line interface.
func Execute() {
	if err := rootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// rootCommand builds the command tree.
func rootCommand() *cobra.Command {
	global := &globalOptions{}

	cmd := &cobra.Command{
		Use:   "obscli",
		Short: "Declarative management of Open Build Service projects",
		Long: "obscli manages the Open Build Service projects of the Kubernetes package\n" +
			"repositories declaratively. The YAML configuration in git is the desired\n" +
			"state, and apply reconciles the live OBS state with it.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().StringVar(
		&global.apiURL, "api-url", obs.DefaultAPIURL, "base URL of the OBS API",
	)

	cmd.AddCommand(
		applyCommand(global),
		validateCommand(),
		exportCommand(global),
		newVersionCommand(),
	)

	return cmd
}

// newClient builds an authenticated OBS client. The dry parameter decides
// whether mutating calls are carried out or suppressed.
func newClient(global *globalOptions, dry bool) (*obs.OBS, error) {
	username, password := os.Getenv(usernameEnv), os.Getenv(passwordEnv)
	if username == "" || password == "" {
		return nil, fmt.Errorf(
			"OBS credentials are missing, set the %s and %s environment variables",
			usernameEnv, passwordEnv,
		)
	}

	options := obs.DefaultOptions()
	options.Username = username
	options.Password = password
	options.APIURL = global.apiURL
	options.Dry = dry

	return obs.New(options), nil
}

// explainError adds the hint only the command line can give: which environment
// variables carry the credentials OBS rejected. Without it a 401 says nothing
// about where the wrong credentials came from.
func explainError(err error) error {
	if !obs.IsUnauthorized(err) {
		return err
	}

	return fmt.Errorf(
		"%w, check the %s and %s environment variables",
		err, usernameEnv, passwordEnv,
	)
}
