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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

type githubStep struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Uses string            `json:"uses"`
	Run  string            `json:"run"`
	If   string            `json:"if"`
	With map[string]any    `json:"with"`
	Env  map[string]string `json:"env"`
}

type githubJob struct {
	If          string            `json:"if"`
	Needs       any               `json:"needs"`
	Permissions map[string]string `json:"permissions"`
	Steps       []githubStep      `json:"steps"`
	Strategy    struct {
		Matrix map[string]any `json:"matrix"`
	} `json:"strategy"`
}

type githubWorkflow struct {
	Permissions map[string]string    `json:"permissions"`
	Jobs        map[string]githubJob `json:"jobs"`
}

func readWorkflow(t *testing.T, name string) githubWorkflow {
	t.Helper()
	data, err := os.ReadFile("../.github/workflows/" + name + ".yml")
	require.NoError(t, err)
	var workflow githubWorkflow
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	assert.NotContains(t, string(data), "pull_request_target:", "untrusted PR code must not receive privileged tokens")
	return workflow
}

func TestGitHubWorkflowActionsArePinnedAndCredentialsAreNotPersisted(t *testing.T) {
	pin := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	for _, name := range []string{"verify", "commit-lint", "security", "release-please", "release"} {
		t.Run(name, func(t *testing.T) {
			workflow := readWorkflow(t, name)
			for jobName, job := range workflow.Jobs {
				for _, step := range job.Steps {
					if step.Uses == "" || strings.HasPrefix(step.Uses, "./") {
						continue
					}
					assert.Regexp(t, pin, step.Uses, "%s: %s", jobName, step.Name)
					if strings.HasPrefix(step.Uses, "actions/checkout@") {
						assert.Equal(t, false, step.With["persist-credentials"], jobName)
					}
				}
			}
		})
	}
}

func TestGitHubCIPreservesCompositeVerification(t *testing.T) {
	workflow := readWorkflow(t, "verify")
	assert.Equal(t, map[string]string{"contents": "read"}, workflow.Permissions)
	for _, job := range []string{"check", "helm", "docker"} {
		assert.Contains(t, workflow.Jobs, job)
	}
	check := workflow.Jobs["check"]
	var commands strings.Builder
	for _, step := range check.Steps {
		commands.WriteString(step.Run)
		commands.WriteByte('\n')
		assert.NotContains(t, step.Run, "make gen", "CI must not repair generated files before verifying them")
	}
	for _, command := range []string{"verify-headers", "verify-api-deps", "verify-generated", "verify-crd-labels", "lint-api", "make test COVERPROFILE=coverage.out", "make build"} {
		assert.Contains(t, commands.String(), command)
	}
}

func TestGitHubSecurityScansBothModulesAndGatesSARIFUploads(t *testing.T) {
	workflow := readWorkflow(t, "security")
	assert.Equal(t, map[string]string{"contents": "read"}, workflow.Permissions)
	assert.Equal(t, []any{".", "api"}, workflow.Jobs["govulncheck"].Strategy.Matrix["module"])
	for _, name := range []string{"govulncheck", "gosec", "gitleaks", "trivy-fs", "trivy-image"} {
		job, found := workflow.Jobs[name]
		require.True(t, found, name)
		assert.NotEqual(t, "write", job.Permissions["security-events"], "PR scanners must not receive write tokens")
	}
	var gosecCommands strings.Builder
	for _, step := range workflow.Jobs["gosec"].Steps {
		gosecCommands.WriteString(step.Run)
	}
	assert.Contains(t, gosecCommands.String(), "go run ./hack/normalize-sarif -prefix api", "API findings need repository-relative source paths")
	upload, found := workflow.Jobs["code-scanning"]
	require.True(t, found)
	assert.Equal(t, "write", upload.Permissions["security-events"])
	assert.Equal(t, "read", upload.Permissions["actions"], "Code Scanning must read workflow metadata in private repositories")
	assert.Contains(t, upload.If, "vars.ENABLE_CODE_SCANNING == 'true'")
	assert.Contains(t, upload.If, "github.event_name != 'pull_request'")
}

func TestGitHubReleasePublishesOnlyAfterValidationAndImages(t *testing.T) {
	workflow := readWorkflow(t, "release")
	assert.Equal(t, map[string]string{"contents": "read"}, workflow.Permissions)
	assert.Equal(t, "validate", workflow.Jobs["images"].Needs)
	assert.Equal(t, []any{"validate", "images"}, workflow.Jobs["artifacts"].Needs)
	assert.Equal(t, []any{"validate", "images"}, workflow.Jobs["chart"].Needs)
	for _, step := range workflow.Jobs["validate"].Steps {
		assert.NotContains(t, step.Uses, "docker/login-action")
	}
	found := false
	for _, step := range workflow.Jobs["artifacts"].Steps {
		if strings.HasPrefix(step.Uses, "goreleaser/goreleaser-action@") {
			found = true
			assert.Equal(
				t,
				"${{ needs.validate.outputs.version }}",
				step.Env["GORELEASER_CURRENT_TAG"],
				"another tag at HEAD must not change the release version",
			)
		}
	}
	assert.True(t, found, "the binary publisher must pin GoReleaser to the validated event tag")
}

func TestReleasePleaseRequiresCompleteAppConfiguration(t *testing.T) {
	workflow := readWorkflow(t, "release-please")
	assert.Equal(t, map[string]string{"contents": "read"}, workflow.Permissions)
	steps := workflow.Jobs["release-please"].Steps
	require.NotEmpty(t, steps)
	configuration := steps[0]
	require.Equal(t, "app-config", configuration.ID, "check optional credentials before requesting an App token")
	assert.Equal(t, "${{ secrets.RELEASE_PLEASE_APP_ID }}", configuration.Env["APP_ID"])
	assert.Equal(t, "${{ secrets.RELEASE_PLEASE_APP_PRIVATE_KEY }}", configuration.Env["APP_PRIVATE_KEY"])
	require.Len(t, steps, 3)
	for _, step := range steps[1:] {
		assert.Equal(t, "steps.app-config.outputs.configured == 'true'", step.If)
	}
	assert.Equal(t, "${{ steps.app-token.outputs.token }}", steps[2].With["token"], "do not fall back to a token that suppresses follow-up workflows")
	assert.Equal(t, "main", steps[2].With["target-branch"], "release the triggering branch, not a different repository default")

	for _, tc := range []struct {
		name, appID, privateKey, configured string
		wantError                           bool
	}{
		{name: "not-configured", configured: "false"},
		{name: "configured", appID: "123", privateKey: "test-private-key", configured: "true"},
		{name: "missing-private-key", appID: "123", wantError: true},
		{name: "missing-app-id", privateKey: "test-private-key", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			outputPath := filepath.Join(dir, "output")
			summaryPath := filepath.Join(dir, "summary")
			require.NoError(t, os.WriteFile(outputPath, nil, 0o600))
			require.NoError(t, os.WriteFile(summaryPath, nil, 0o600))
			t.Setenv("APP_ID", tc.appID)
			t.Setenv("APP_PRIVATE_KEY", tc.privateKey)
			t.Setenv("GITHUB_OUTPUT", outputPath)
			t.Setenv("GITHUB_STEP_SUMMARY", summaryPath)
			cmd := exec.CommandContext(t.Context(), "bash", "-c", configuration.Run)
			log, err := cmd.CombinedOutput()
			if tc.wantError {
				require.Error(t, err, "%s", log)
				assert.Contains(t, string(log), "::error::")
			} else {
				require.NoError(t, err, "%s", log)
			}
			output, err := os.ReadFile(outputPath)
			require.NoError(t, err)
			if tc.wantError {
				assert.Empty(t, output, "partial credentials must not enable release steps")
			} else {
				assert.Equal(t, "configured="+tc.configured+"\n", string(output))
			}
			summary, err := os.ReadFile(summaryPath)
			require.NoError(t, err)
			if tc.configured == "false" {
				assert.Contains(t, string(log), "::notice::")
				assert.Contains(t, string(summary), "not configured")
			}
			assert.NotContains(t, string(log)+string(output)+string(summary), "test-private-key")
		})
	}
}

func TestReleasePleaseKeepsOneProviderAndChartVersion(t *testing.T) {
	manifestData, err := os.ReadFile("../.release-please-manifest.json")
	require.NoError(t, err)
	var manifest map[string]string
	require.NoError(t, json.Unmarshal(manifestData, &manifest))
	require.Len(t, manifest, 1)
	chartData, err := os.ReadFile("../helm/Chart.yaml")
	require.NoError(t, err)
	var chart struct {
		Version    string `json:"version"`
		AppVersion string `json:"appVersion"`
	}
	require.NoError(t, yaml.Unmarshal(chartData, &chart))
	assert.Equal(t, manifest["."], chart.Version)
	assert.Equal(t, manifest["."], strings.TrimPrefix(chart.AppVersion, "v"))
	configData, err := os.ReadFile("../release-please-config.json")
	require.NoError(t, err)
	var config struct {
		Packages map[string]struct {
			Prerelease bool `json:"prerelease"`
			ExtraFiles []struct {
				Path string `json:"path"`
			} `json:"extra-files"`
		} `json:"packages"`
	}
	require.NoError(t, json.Unmarshal(configData, &config))
	require.Len(t, config.Packages, 1)
	assert.True(t, config.Packages["."].Prerelease)
	paths := make([]string, 0, len(config.Packages["."].ExtraFiles))
	for _, file := range config.Packages["."].ExtraFiles {
		paths = append(paths, file.Path)
	}
	assert.Contains(t, paths, "helm/Chart.yaml")
	assert.Contains(t, paths, "Makefile")
}
