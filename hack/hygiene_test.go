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

package hack_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHygieneRejectsTrackedPrivatePaths(t *testing.T) {
	for _, path := range []string{
		".env", "local/.env", "local/.env.production", "local/config with spaces/.env",
		"access.kubeconfig", "local/access.kubeconfig", "local/kubeconfig",
		".kube/config", "local/.kube/config", "local/client.key", "local/client.pem",
		"local/client.p12", "local/client.pfx", "local/terraform.tfstate",
		"local/terraform.tfstate.backup", "local/terraform.tfvars", "local/terraform.tfvars.json",
		".tools/auth/hosts.yml",
	} {
		t.Run(path, func(t *testing.T) {
			root := hygieneFixture(t)
			file := filepath.Join(root, filepath.FromSlash(path))
			require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
			const payload = "private-fixture-must-not-be-printed"
			require.NoError(t, os.WriteFile(file, []byte(payload), 0o600))
			hygieneGit(t, root, "check-ignore", "--quiet", "--", path)
			hygieneGit(t, root, "add", "--force", "--", path)
			output, err := runHygiene(t, root)
			require.Error(t, err)
			assert.Contains(t, string(output), path)
			assert.NotContains(t, string(output), payload)
		})
	}
}

func TestHygieneAllowsExamplesAndRemovedPrivatePaths(t *testing.T) {
	root := hygieneFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "example.yaml"), []byte("kind: CompositeProvider\n"), 0o600))
	hygieneGit(t, root, "add", "example.yaml")
	output, err := runHygiene(t, root)
	require.NoError(t, err, "%s", output)
	file := filepath.Join(root, ".env")
	require.NoError(t, os.WriteFile(file, []byte("private fixture"), 0o600))
	hygieneGit(t, root, "add", "--force", ".env")
	require.NoError(t, os.Remove(file))
	output, err = runHygiene(t, root)
	require.NoError(t, err, "%s", output)
}

func TestHygieneSecretRules(t *testing.T) {
	scanner := os.Getenv("GITLEAKS")
	if scanner == "" {
		t.Skip("make verify-hygiene supplies the pinned GITLEAKS binary")
	}
	scanner, err := filepath.Abs(scanner)
	require.NoError(t, err)
	config, err := filepath.Abs("../.gitleaks.toml")
	require.NoError(t, err)
	contacts, err := os.ReadFile("../MAINTAINERS.md")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, input, rule string
	}{
		{"internal-host", strings.Join([]string{"api", "fixture", "internal"}, "."), "private-infrastructure-reference"},
		{"corporate-host", strings.Join([]string{"api", "fixture", "corp"}, "."), "private-infrastructure-reference"},
		{"tooling-host", strings.Join([]string{"api", "tooling", "fixture", "cloud"}, "."), "private-infrastructure-reference"},
		{"mac-home", strings.Join([]string{"", "Users", "fixture-person", "project"}, "/"), "personal-workstation-path"},
		{"linux-home", strings.Join([]string{"", "home", "fixture-person", "project"}, "/"), "personal-workstation-path"},
		{"github-token", "gh" + "p_" + "abcdefghijklmnopqrstuvwxyz0123456789", "github-pat"},
		{"reserved-example", "https://api.example.com\n203.0.113.10\nname: demo-credentials", ""},
		{"reserved-internal-label", strings.Join([]string{"api", "internal", "example", "com"}, "."), ""},
		{"reserved-tooling-label", strings.Join([]string{"tooling", "fixture", "cloud", "example", "com"}, "."), ""},
		{"portable-path", "KUBECONFIG=/path/to/management.yaml\n${HOME}/project", ""},
		{"approved-contacts", string(contacts), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			report := filepath.Join(root, "report.json")
			cmd := exec.CommandContext(t.Context(), scanner, "stdin", "--config", config,
				"--redact=100", "--no-banner", "--no-color", "--ignore-gitleaks-allow",
				"--report-format", "json", "--report-path", report)
			cmd.Dir, cmd.Stdin = root, strings.NewReader(tc.input+"\n")
			output, scanErr := cmd.CombinedOutput()
			if tc.rule == "" {
				require.NoError(t, scanErr, "%s", output)
			} else {
				var exitErr *exec.ExitError
				require.ErrorAs(t, scanErr, &exitErr, "%s", output)
				require.Equal(t, 1, exitErr.ExitCode(), "%s", output)
			}
			data, err := os.ReadFile(report)
			require.NoError(t, err)
			var findings []struct {
				RuleID string
				Secret string
			}
			require.NoError(t, json.Unmarshal(data, &findings))
			if tc.rule == "" {
				assert.Empty(t, findings)
			} else {
				require.Len(t, findings, 1)
				assert.Equal(t, tc.rule, findings[0].RuleID)
				assert.Equal(t, "REDACTED", findings[0].Secret)
			}
		})
	}
}

func hygieneFixture(t *testing.T) string {
	t.Helper()
	root := policyFixture(t, "verify-hygiene.sh")
	ignore, err := os.ReadFile("../.gitignore")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), ignore, 0o600))
	t.Setenv("ACTIONLINT", policyCommand(t, root, "fake-actionlint", ""))
	t.Setenv("GITLEAKS", policyCommand(t, root, "fake-gitleaks", ""))
	hygieneGit(t, root, "init", "--quiet")
	return root
}

func hygieneGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func runHygiene(t *testing.T, root string) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "hack", "verify-hygiene.sh"))
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("running hygiene check: %w", err)
	}
	return output, nil
}
