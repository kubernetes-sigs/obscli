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
	"regexp"
	"strings"

	"sigs.k8s.io/release-sdk/obs"
)

// User and group expressions mirror the name validation of the OBS API.
// See src/api/app/models/{user,group}.rb of
// openSUSE/open-build-service.
var (
	userNameRegex  = regexp.MustCompile(`^[\w $^\-.#*+&'"]+$`)
	groupNameRegex = regexp.MustCompile(`^[\w.-]+$`)
)

// ValidProjectName reports whether name is an acceptable OBS project name.
func ValidProjectName(name string) bool {
	return obs.ValidateProjectName(name) == nil
}

// Ancestors returns every ancestor of a project name, from the closest one up
// to the bare top level name.
func Ancestors(name string) []string {
	var result []string

	for {
		index := strings.LastIndex(name, ":")
		if index < 0 {
			return result
		}

		name = name[:index]
		result = append(result, name)
	}
}

// ValidPackageName reports whether name is an acceptable OBS package name.
func ValidPackageName(name string) bool {
	return obs.ValidatePackageName(name) == nil
}

// ValidUserName reports whether name is an acceptable OBS user login.
func ValidUserName(name string) bool {
	if len(name) < 2 || len(name) > 100 {
		return false
	}

	return userNameRegex.MatchString(name)
}

// ValidGroupName reports whether name is an acceptable OBS group title.
func ValidGroupName(name string) bool {
	if name == "" || len(name) > 100 {
		return false
	}

	return groupNameRegex.MatchString(name)
}
