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

// Package hack_test verifies the local tool-policy entrypoints without network access.
package hack_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestToolPolicy_RequiresSelectedVersions(t *testing.T) {
	root := policyFixture(t, "verify-tools.sh")
	goCommand := policyCommand(t, root, "fake-go", "go1.26.10")
	lintCommand := policyCommand(t, root, "fake-lint", "golangci-lint has version 2.12.2 built with go1.26.8")
	helmCommand := policyCommand(t, root, "fake-helm", "v4.2.0+test")
	t.Setenv("GO", goCommand)
	t.Setenv("GOLANGCI_LINT", lintCommand)
	t.Setenv("HELM", helmCommand)
	_, err := runPolicy(t, root, "verify-tools.sh")
	require.NoError(t, err)
	policyCommand(t, root, "fake-helm", "v3.19.0")
	output, err := runPolicy(t, root, "verify-tools.sh")
	require.Error(t, err)
	assert.Contains(t, string(output), "v4.2.0")
	policyCommand(t, root, "fake-helm", "v4.2.0")
	policyCommand(t, root, "fake-lint", "golangci-lint has version 2.12.20 built with go1.26.8")
	_, err = runPolicy(t, root, "verify-tools.sh")
	require.Error(t, err)
}

func TestAPIDependencyPolicy_RejectsRuntimeAndToolModules(t *testing.T) {
	root := policyFixture(t, "verify-api-deps.sh")
	require.NoError(t, os.Mkdir(filepath.Join(root, "api"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "api", "go.mod"), []byte("module example.org/api\ngo 1.26.0\n"), 0o600))
	goCommand := policyCommand(t, root, "fake-go", "example.org/api\nk8s.io/apimachinery v0.34.3")
	t.Setenv("GO", goCommand)
	_, err := runPolicy(t, root, "verify-api-deps.sh")
	require.NoError(t, err)
	for _, forbidden := range []string{
		"sigs.k8s.io/cluster-api", "sigs.k8s.io/controller-runtime", "sigs.k8s.io/controller-tools", "sigs.k8s.io/kube-api-linter",
		"github.com/golangci/golangci-lint/v2", "github.com/golangci/plugin-module-register",
	} {
		policyCommand(t, root, "fake-go", "example.org/api\n"+forbidden+" v1.0.0")
		_, err = runPolicy(t, root, "verify-api-deps.sh")
		require.Error(t, err, forbidden)
	}
	policyCommand(t, root, "fake-go", "example.org/api")
	require.NoError(
		t,
		os.WriteFile(filepath.Join(root, "api", "go.mod"), []byte("module example.org/api\ngo 1.26.0\ntool example.org/generator\n"), 0o600),
	)
	_, err = runPolicy(t, root, "verify-api-deps.sh")
	require.Error(t, err)
}

func TestAPILinterPinsMatchToolPolicy(t *testing.T) {
	data, err := os.ReadFile("../.custom-gcl.yml")
	require.NoError(t, err)
	var config struct {
		Version string `json:"version"`
		Plugins []struct {
			Module  string `json:"module"`
			Version string `json:"version"`
		} `json:"plugins"`
	}
	require.NoError(t, yaml.Unmarshal(data, &config))
	policy, err := os.ReadFile("tools.env")
	require.NoError(t, err)
	assert.Contains(t, string(policy), "GOLANGCI_LINT_VERSION="+strings.TrimPrefix(config.Version, "v")+"\n")
	require.Len(t, config.Plugins, 1)
	assert.Equal(t, "sigs.k8s.io/kube-api-linter", config.Plugins[0].Module)
	assert.Contains(t, string(policy), "KUBE_API_LINTER_VERSION="+config.Plugins[0].Version+"\n")
}

func policyFixture(t *testing.T, script string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "hack"), 0o700))
	for _, name := range []string{script, "tools.env"} {
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, "hack", name), data, 0o600))
	}
	return root
}

func policyCommand(t *testing.T, root, name, output string) string {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+output+"'\n"), 0o700))
	return path
}

func runPolicy(t *testing.T, root, name string) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", filepath.Join(root, "hack", name))
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("running policy check: %w", err)
	}
	return output, nil
}
