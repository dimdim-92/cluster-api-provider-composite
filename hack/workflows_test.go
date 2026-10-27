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
	Uses        string            `json:"uses"`
	Environment string            `json:"environment"`
	With        map[string]any    `json:"with"`
	If          string            `json:"if"`
	Needs       any               `json:"needs"`
	Permissions map[string]string `json:"permissions"`
	Steps       []githubStep      `json:"steps"`
	Strategy    struct {
		Matrix map[string]any `json:"matrix"`
	} `json:"strategy"`
}

type githubWorkflow struct {
	On          map[string]any       `json:"on"`
	YAMLOn      map[string]any       `json:"true"` // YAML 1.1 treats unquoted on as a boolean key.
	Permissions map[string]string    `json:"permissions"`
	Jobs        map[string]githubJob `json:"jobs"`
}

func readWorkflow(t *testing.T, name string) githubWorkflow {
	t.Helper()
	data, err := os.ReadFile("../.github/workflows/" + name + ".yml")
	require.NoError(t, err)
	var workflow githubWorkflow
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	if workflow.On == nil {
		workflow.On = workflow.YAMLOn
	}
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
	assert.Equal(t, []any{"validate", "verify", "security"}, workflow.Jobs["images"].Needs)
	assert.Equal(t, []any{"validate", "images"}, workflow.Jobs["package"].Needs)
	assert.Equal(t, []any{"validate", "package"}, workflow.Jobs["artifacts"].Needs)
	assert.Equal(t, []any{"validate", "package"}, workflow.Jobs["chart"].Needs)
	for _, name := range []string{"images", "artifacts", "chart"} {
		assert.Equal(t, "release", workflow.Jobs[name].Environment, "publication needs an independently protected environment")
	}
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

func TestReleaseReusesVerificationAndPromotesOnlySameRunArtifacts(t *testing.T) {
	workflow := readWorkflow(t, "release")
	require.Contains(t, workflow.On, "release")
	assert.NotContains(t, workflow.On, "push", "a tag alone must not publish a second time")
	releaseTrigger, ok := workflow.On["release"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"published"}, releaseTrigger["types"], "publish stable AND alpha releases")
	assert.Equal(t, "./.github/workflows/verify.yml", workflow.Jobs["verify"].Uses)
	assert.Equal(t, "validate", workflow.Jobs["verify"].Needs)
	assert.Equal(t, "${{ needs.validate.outputs.version }}", workflow.Jobs["verify"].With["release-version"])
	assert.Equal(t, "./.github/workflows/security.yml", workflow.Jobs["security"].Uses)
	assert.Equal(t, []any{"validate", "verify"}, workflow.Jobs["security"].Needs)
	assert.Equal(t, "${{ needs.validate.outputs.version }}", workflow.Jobs["security"].With["release-version"])

	for _, name := range []string{"verify", "security"} {
		called := readWorkflow(t, name)
		assert.Contains(t, called.On, "workflow_call", name)
	}
	images := workflow.Jobs["images"]
	var imageCommands strings.Builder
	for _, step := range images.Steps {
		assert.NotContains(t, step.Uses, "docker/build-push-action", "publication must not rebuild verified images")
		if step.ID == "meta" {
			tags, ok := step.With["tags"].(string)
			require.True(t, ok)
			assert.Contains(t, tags, "type=raw,value=latest,enable=${{ !contains(needs.validate.outputs.version, '-') }}")
			assert.Contains(t, tags, "type=semver,pattern={{major}}.{{minor}},enable=${{ !contains(needs.validate.outputs.version, '-') }}")
			assert.Contains(t, tags, "!startsWith(needs.validate.outputs.version, 'v0.')")
		}
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") {
			assert.Equal(t, "operator-${{ github.sha }}-*", step.With["pattern"])
			assert.NotContains(t, step.With, "run-id", "publish only artifacts from this verified workflow run")
			assert.NotContains(t, step.With, "repository")
		}
		imageCommands.WriteString(step.Run)
	}
	assert.Contains(t, imageCommands.String(), "hack/release-images.sh publish")

	for _, name := range []string{"artifacts", "chart"} {
		found := false
		for _, step := range workflow.Jobs[name].Steps {
			assert.NotContains(t, step.Run, "make helm-package", "reuse one packaged chart for both publishers")
			if strings.HasPrefix(step.Uses, "actions/download-artifact@") {
				found = true
				assert.Equal(t, "release-package-${{ github.sha }}", step.With["name"])
				assert.Equal(t, "out", step.With["path"])
			}
		}
		assert.True(t, found, name)
	}
}

func TestReleaseCheckoutsPinTheEventCommit(t *testing.T) {
	for _, name := range []string{"release", "verify", "security"} {
		for jobName, job := range readWorkflow(t, name).Jobs {
			for _, step := range job.Steps {
				if strings.HasPrefix(step.Uses, "actions/checkout@") {
					assert.Equal(t, "${{ github.sha }}", step.With["ref"], "%s/%s must use the event commit, not a movable ref", name, jobName)
				}
			}
		}
	}
}

func TestReleaseGuardRejectsWrongEventRefAndUnreviewedCommit(t *testing.T) {
	steps := readWorkflow(t, "release").Jobs["validate"].Steps
	require.GreaterOrEqual(t, len(steps), 3)
	require.Equal(t, "guard", steps[1].ID, "guard must run immediately after checkout, before repository tools")
	require.Equal(t, "./.github/actions/setup-tools", steps[2].Uses)
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, event, action, draft, ref, tagSHA, ancestry string
		wantError                                         bool
	}{
		{name: "published", event: "release", action: "published", draft: "false", ref: "refs/tags/v0.1.0", tagSHA: sha, ancestry: "0"},
		{name: "tag-push", event: "push", action: "published", draft: "false", ref: "refs/tags/v0.1.0", tagSHA: sha, ancestry: "0", wantError: true},
		{name: "wrong-action", event: "release", action: "edited", draft: "false", ref: "refs/tags/v0.1.0", tagSHA: sha, ancestry: "0", wantError: true},
		{name: "draft", event: "release", action: "published", draft: "true", ref: "refs/tags/v0.1.0", tagSHA: sha, ancestry: "0", wantError: true},
		{name: "wrong-ref", event: "release", action: "published", draft: "false", ref: "refs/heads/main", tagSHA: sha, ancestry: "0", wantError: true},
		{name: "moved-tag", event: "release", action: "published", draft: "false", ref: "refs/tags/v0.1.0", tagSHA: strings.Repeat("b", 40), ancestry: "0", wantError: true},
		{name: "not-on-main", event: "release", action: "published", draft: "false", ref: "refs/tags/v0.1.0", tagSHA: sha, ancestry: "1", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			git := "#!/bin/sh\ncase \"$1\" in\ncheck-ref-format) exit 0;;\nrev-parse) printf '%s\\n' \"$TEST_TAG_SHA\";;\nmerge-base) exit \"$TEST_ANCESTRY\";;\n*) exit 2;;\nesac\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(git), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GITHUB_EVENT_NAME", tc.event)
			t.Setenv("RELEASE_ACTION", tc.action)
			t.Setenv("RELEASE_DRAFT", tc.draft)
			t.Setenv("TAG", "v0.1.0")
			t.Setenv("GITHUB_REF", tc.ref)
			t.Setenv("GITHUB_SHA", sha)
			t.Setenv("TEST_TAG_SHA", tc.tagSHA)
			t.Setenv("TEST_ANCESTRY", tc.ancestry)
			output, err := exec.CommandContext(t.Context(), "bash", "-c", steps[1].Run).CombinedOutput()
			if tc.wantError {
				require.Error(t, err, "%s", output)
			} else {
				require.NoError(t, err, "%s", output)
			}
		})
	}
}

func TestReleasePrereleaseStateMatchesTheValidatedVersion(t *testing.T) {
	var release githubStep
	for _, step := range readWorkflow(t, "release").Jobs["validate"].Steps {
		if step.ID == "release" {
			release = step
		}
	}
	require.NotEmpty(t, release.Run)
	for _, tc := range []struct {
		name, tag, prerelease string
		wantError             bool
	}{
		{name: "stable", tag: "v0.1.0", prerelease: "false"},
		{name: "alpha", tag: "v0.1.0-alpha.3", prerelease: "true"},
		{name: "stable-marked-prerelease", tag: "v0.1.0", prerelease: "true", wantError: true},
		{name: "alpha-marked-stable", tag: "v0.1.0-alpha.3", prerelease: "false", wantError: true},
		{name: "missing-state", tag: "v0.1.0", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, "hack"), 0o700))
			// Isolate event-state policy; check-release's own tests exercise strict SemVer and metadata.
			verifier := "#!/bin/sh\ntest \"$VERSION\" = \"$TAG\" && test \"$IMAGE\" = \"ghcr.io/numspot/cluster-api-provider-composite:$TAG\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "hack", "verify-version.sh"), []byte(verifier), 0o600))
			outputPath := filepath.Join(dir, "output")
			require.NoError(t, os.WriteFile(outputPath, nil, 0o600))
			t.Setenv("TAG", tc.tag)
			t.Setenv("RELEASE_PRERELEASE", tc.prerelease)
			t.Setenv("GITHUB_REPOSITORY", "Numspot/Cluster-API-Provider-Composite")
			t.Setenv("GITHUB_OUTPUT", outputPath)
			cmd := exec.CommandContext(t.Context(), "bash", "-c", release.Run)
			cmd.Dir = dir
			log, err := cmd.CombinedOutput()
			data, readErr := os.ReadFile(outputPath)
			require.NoError(t, readErr)
			if tc.wantError {
				require.Error(t, err, "%s", log)
				assert.Empty(t, data, "invalid state must not enable downstream publication")
			} else {
				require.NoError(t, err, "%s", log)
				assert.Equal(t, "version="+tc.tag+"\nimage=ghcr.io/numspot/cluster-api-provider-composite\n", string(data))
			}
		})
	}
}

func TestReleaseBuildsAndScansBothImageArchitectures(t *testing.T) {
	ci := readWorkflow(t, "verify").Jobs["docker"]
	assert.Contains(t, ci.Strategy.Matrix["arch"], "amd64")
	assert.Contains(t, ci.Strategy.Matrix["arch"], "arm64")
	imageArtifact := "operator-${{ github.sha }}-${{ matrix.arch }}"
	foundBuild, foundArtifact := false, false
	for _, step := range ci.Steps {
		if strings.HasPrefix(step.Uses, "docker/build-push-action@") {
			foundBuild = true
			assert.Equal(t, false, step.With["push"])
			assert.Equal(t, true, step.With["load"])
			assert.Equal(t, "linux/${{ matrix.arch }}", step.With["platforms"])
			assert.Contains(t, step.With["build-args"], "version=${{ env.BUILD_VERSION }}")
			assert.Contains(t, step.With["labels"], "https://github.com/${{ github.repository }}")
		}
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			foundArtifact = true
			assert.Equal(t, "inputs.release-version != ''", step.If)
			assert.Equal(t, imageArtifact, step.With["name"])
			assert.Equal(t, "error", step.With["if-no-files-found"])
		}
	}
	assert.True(t, foundBuild)
	assert.True(t, foundArtifact)
	scan := readWorkflow(t, "security").Jobs["trivy-image"]
	assert.Equal(t, ci.Strategy.Matrix["arch"], scan.Strategy.Matrix["arch"])
	foundDownload, foundScan := false, false
	for _, step := range scan.Steps {
		if strings.HasPrefix(step.Uses, "docker/build-push-action@") {
			assert.Equal(t, "inputs.release-version == ''", step.If, "release scans must not rebuild images")
		}
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") {
			foundDownload = true
			assert.Equal(t, imageArtifact, step.With["name"])
		}
		if strings.HasPrefix(step.Uses, "aquasecurity/trivy-action@") {
			foundScan = true
			assert.Equal(t, "composite-ci:${{ github.sha }}-${{ matrix.arch }}", step.With["image-ref"])
			assert.Equal(t, "1", step.With["exit-code"])
		}
	}
	assert.True(t, foundDownload)
	assert.True(t, foundScan)
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
	makefile, err := os.ReadFile("../Makefile")
	require.NoError(t, err)
	assert.Contains(t, string(makefile), "VERSION ?= v"+manifest["."]+"\n")
	configData, err := os.ReadFile("../release-please-config.json")
	require.NoError(t, err)
	var config struct {
		Packages map[string]struct {
			Prerelease            bool   `json:"prerelease"`
			Versioning            string `json:"versioning"`
			PrereleaseType        string `json:"prerelease-type"`
			IncludeVInTag         bool   `json:"include-v-in-tag"`
			IncludeComponentInTag bool   `json:"include-component-in-tag"`
			ExtraFiles            []struct {
				Path string `json:"path"`
			} `json:"extra-files"`
		} `json:"packages"`
	}
	require.NoError(t, json.Unmarshal(configData, &config))
	require.Len(t, config.Packages, 1)
	assert.True(t, config.Packages["."].Prerelease)
	assert.Equal(t, "prerelease", config.Packages["."].Versioning)
	assert.Equal(t, "alpha", config.Packages["."].PrereleaseType)
	assert.True(t, config.Packages["."].IncludeVInTag)
	assert.False(t, config.Packages["."].IncludeComponentInTag)
	paths := make([]string, 0, len(config.Packages["."].ExtraFiles))
	for _, file := range config.Packages["."].ExtraFiles {
		paths = append(paths, file.Path)
	}
	assert.Contains(t, paths, "helm/Chart.yaml")
	assert.Contains(t, paths, "Makefile")
}
