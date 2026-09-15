// Copyright 2026 Numspot.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command render-rbac makes the Helm rule fragment from controller-gen output.
package main

import (
	"errors"
	"fmt"
	"os"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

var errInvalidInput = errors.New("expected an input ClusterRole and output template path")

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 {
		return errInvalidInput
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return fmt.Errorf("reading role: %w", err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.UnmarshalStrict(data, &role); err != nil {
		return fmt.Errorf("decoding role: %w", err)
	}
	if role.Kind != "ClusterRole" || len(role.Rules) == 0 {
		return errInvalidInput
	}
	rules, err := yaml.Marshal(role.Rules)
	if err != nil {
		return fmt.Errorf("encoding rules: %w", err)
	}
	content := "{{/* Code generated from config/rbac/role.yaml. DO NOT EDIT. */}}\n{{- define \"composite.rbacRules\" -}}\n" + string(
		rules,
	) + "{{- end -}}\n"
	if err := os.WriteFile(args[1], []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing template: %w", err)
	}
	return nil
}
