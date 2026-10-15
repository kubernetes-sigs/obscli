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
	"fmt"

	"gopkg.in/yaml.v3"

	"sigs.k8s.io/release-sdk/obs"
)

// Flags groups the OBS flag elements managed by obscli. Only the four flags
// the OBS web UI exposes are modelled; see docs/design.md for the rest.
type Flags struct {
	Build       Flag `yaml:"build,omitempty"`
	Publish     Flag `yaml:"publish,omitempty"`
	UseForBuild Flag `yaml:"useforbuild,omitempty"`
	DebugInfo   Flag `yaml:"debuginfo,omitempty"`
}

// IsEmpty reports whether no flag carries any rule.
func (f *Flags) IsEmpty() bool {
	return len(f.Build) == 0 &&
		len(f.Publish) == 0 &&
		len(f.UseForBuild) == 0 &&
		len(f.DebugInfo) == 0
}

// Flag is the ordered rule list of a single OBS flag element. It accepts two
// YAML forms, both decoding into the very same representation:
//
//	build: disable                      # scalar shorthand: one unscoped rule
//
//	build:                              # full rule list
//	  - disable                         # unscoped rule
//	  - enable: {repository: rpm}       # scoped rule
//	  - disable: {repository: rpm, arch: s390x}
type Flag []obs.FlagRule

// flagRuleScope is the value of a scoped rule in the full rule list form.
type flagRuleScope struct {
	Repository string `yaml:"repository,omitempty"`
	Arch       string `yaml:"arch,omitempty"`
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (f *Flag) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind { //nolint:exhaustive // Every other kind is rejected by the default case.
	case yaml.ScalarNode:
		rule, err := decodeFlagAction(value)
		if err != nil {
			return err
		}

		*f = Flag{rule}

		return nil
	case yaml.SequenceNode:
		rules := make(Flag, 0, len(value.Content))

		for _, item := range value.Content {
			rule, err := decodeFlagRule(item)
			if err != nil {
				return err
			}

			rules = append(rules, rule)
		}

		*f = rules

		return nil
	default:
		return fmt.Errorf("line %d: flag must be a string or a list of rules", value.Line)
	}
}

// MarshalYAML implements yaml.Marshaler. It emits the scalar shorthand for a
// single unscoped rule and the full rule list otherwise, so that generated
// configuration looks hand-written.
func (f Flag) MarshalYAML() (any, error) {
	if len(f) == 1 && f[0].Repository == "" && f[0].Arch == "" {
		return string(f[0].Action), nil
	}

	items := make([]any, 0, len(f))

	for _, rule := range f {
		if rule.Repository == "" && rule.Arch == "" {
			items = append(items, string(rule.Action))

			continue
		}

		items = append(items, map[string]flagRuleScope{
			string(rule.Action): {Repository: rule.Repository, Arch: rule.Arch},
		})
	}

	return items, nil
}

// decodeFlagRule decodes a single item of the full rule list form, which is
// either a bare action or a single-key mapping from action to its scope.
func decodeFlagRule(node *yaml.Node) (obs.FlagRule, error) {
	switch node.Kind { //nolint:exhaustive // Every other kind is rejected by the default case.
	case yaml.ScalarNode:
		return decodeFlagAction(node)
	case yaml.MappingNode:
		if len(node.Content) != 2 {
			return obs.FlagRule{}, fmt.Errorf(
				"line %d: flag rule must be a mapping with exactly one key (enable or disable)",
				node.Line,
			)
		}

		rule, err := decodeFlagAction(node.Content[0])
		if err != nil {
			return obs.FlagRule{}, err
		}

		scope, err := decodeFlagScope(node.Content[1])
		if err != nil {
			return obs.FlagRule{}, err
		}

		rule.Repository = scope.Repository
		rule.Arch = scope.Arch

		return rule, nil
	default:
		return obs.FlagRule{}, fmt.Errorf(
			"line %d: flag rule must be a string or a single-key mapping",
			node.Line,
		)
	}
}

// decodeFlagAction turns a scalar node into an unscoped rule, rejecting
// anything but the two valid OBS flag actions.
func decodeFlagAction(node *yaml.Node) (obs.FlagRule, error) {
	action := obs.FlagAction(node.Value)
	if action != obs.FlagActionEnable && action != obs.FlagActionDisable {
		return obs.FlagRule{}, fmt.Errorf(
			"line %d: invalid flag action %q, must be one of: %s, %s",
			node.Line, node.Value, obs.FlagActionEnable, obs.FlagActionDisable,
		)
	}

	return obs.FlagRule{Action: action}, nil
}

// decodeFlagScope decodes the scope of a flag rule. It is hand rolled rather
// than delegated to yaml.Node.Decode, because the latter silently accepts
// unknown keys, which would turn a typo into a rule that is not scoped at all.
func decodeFlagScope(node *yaml.Node) (flagRuleScope, error) {
	scope := flagRuleScope{}

	// An empty value, as in "- enable:", scopes the rule to nothing.
	if node.Tag == "!!null" {
		return scope, nil
	}

	if node.Kind != yaml.MappingNode {
		return scope, fmt.Errorf(
			"line %d: flag rule scope must be a mapping of repository and arch", node.Line,
		)
	}

	seen := make(map[string]struct{}, len(node.Content)/2)

	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]

		if value.Kind != yaml.ScalarNode {
			return scope, fmt.Errorf("line %d: %s must be a string", value.Line, key.Value)
		}

		switch key.Value {
		case "repository":
			if _, exists := seen[key.Value]; exists {
				return scope, fmt.Errorf("line %d: duplicate flag rule scope field %q", key.Line, key.Value)
			}

			seen[key.Value] = struct{}{}
			scope.Repository = value.Value
		case "arch":
			if _, exists := seen[key.Value]; exists {
				return scope, fmt.Errorf("line %d: duplicate flag rule scope field %q", key.Line, key.Value)
			}

			seen[key.Value] = struct{}{}
			scope.Arch = value.Value
		default:
			return scope, fmt.Errorf(
				"line %d: unknown flag rule scope field %q, must be one of: repository, arch",
				key.Line, key.Value,
			)
		}
	}

	return scope, nil
}
