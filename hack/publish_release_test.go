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
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	publishReleaseSHA        = "0123456789abcdef0123456789abcdef01234567"
	publishReleaseRepository = "ExampleOwner/Composite-Operator"
	publishReleaseOutput     = "existing=preserved\n"
	publishReleaseToken      = "test-only-token-never-log-this"
	publishReleaseNotes      = "Private fixture release notes: never dump this payload."
)

func TestPublishReleaseDraft(t *testing.T) {
	for _, tc := range []struct{ version, server string }{
		{"v0.1.0", "https://github.com"},
		{"v0.1.0-rc.1", "https://github.com"},
		{"v0.1.0", "https://github.example.org:8443"},
	} {
		t.Run(tc.version+"-"+tc.server, func(t *testing.T) {
			f := newPublishReleaseFixture(t)
			f.prepare(t, tc.version, tc.server, false)
			output, err := f.run(t, nil)
			require.NoError(t, err, "%s", output)
			assert.Empty(t, output, "API payloads and notes are not console output")
			f.assertOutput(t, publishReleaseOutput+"url="+f.releaseURL()+"\n")
			assert.Len(t, f.calls(t), 6)
			assert.Equal(t, 1, f.writes(t))
		})
	}
}

func TestPublishReleaseDefaultServerAndAssetOrdering(t *testing.T) {
	f := newPublishReleaseFixture(t)
	url := f.releaseURL()
	delete(f.env, "GITHUB_SERVER_URL")
	f.mutate(t, 4, func(t *testing.T, r map[string]any) {
		assets := publishReleaseAssets(t, r)
		slices.Reverse(assets)
		publishReleaseObject(t, assets[0])["download_count"] = 5
	})
	output, err := f.run(t, nil)
	require.NoError(t, err, "%s", output)
	f.assertOutput(t, publishReleaseOutput+"url="+url+"\n")
}

func TestPublishReleaseImmutableRetry(t *testing.T) {
	f := newPublishReleaseFixture(t)
	f.prepare(t, "v0.1.0", "https://github.com", true)
	output, err := f.run(t, nil)
	require.NoError(t, err, "%s", output)
	assert.Empty(t, output)
	f.assertOutput(t, publishReleaseOutput+"url="+f.releaseURL()+"\n")
	assert.Len(t, f.calls(t), 5, "re-read the release and tag even on a final-job retry")
	assert.Zero(t, f.writes(t), "immutable releases must never be patched")
}

func TestPublishReleaseChecksAndBindsDraftBeforeAssetsExist(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprint(bound), func(t *testing.T) {
			f := newPublishReleaseFixture(t)
			f.asDraftCheck()
			if !bound {
				delete(f.env, "RELEASE_ID")
			}
			f.mutate(t, 2, func(_ *testing.T, r map[string]any) { r["assets"] = []any{} })
			output, err := f.run(t, nil, "check-draft")
			require.NoError(t, err, "%s", output)
			assert.Empty(t, output)
			f.assertOutput(t, publishReleaseOutput+"release_id=42\n")
			assert.Len(t, f.calls(t), 4)
			assert.Zero(t, f.writes(t), "the protected draft gate is metadata verification, not publication")
		})
	}
}

func TestPublishReleaseDraftCheckAllowsKnownPartialUploads(t *testing.T) {
	f := newPublishReleaseFixture(t)
	f.asDraftCheck()
	f.mutate(t, 2, func(_ *testing.T, r map[string]any) {
		r["assets"] = []any{map[string]any{"id": 1, "name": "checksums.txt", "state": "starter", "size": 0}}
	})
	output, err := f.run(t, nil, "check-draft")
	require.NoError(t, err, "%s", output)
	assert.Zero(t, f.writes(t))
	f.assertOutput(t, publishReleaseOutput+"release_id=42\n")
}

func TestPublishReleaseDraftCheckRejectsUnsafeState(t *testing.T) {
	for _, tc := range []publishReleaseMutation{
		{"published", func(_ *testing.T, r map[string]any) { r["draft"], r["immutable"] = false, true }},
		{"immutable-draft", func(_ *testing.T, r map[string]any) { r["immutable"] = true }},
		{"wrong-title", func(_ *testing.T, r map[string]any) { r["name"] = "wrong title" }},
		{"unknown-upload", func(_ *testing.T, r map[string]any) {
			r["assets"] = []any{map[string]any{"id": 99, "name": "unreviewed.txt"}}
		}},
		{"ambiguous-upload", func(t *testing.T, r map[string]any) {
			assets := publishReleaseAssets(t, r)
			assets[1] = assets[0]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPublishReleaseFixture(t)
			f.asDraftCheck()
			f.mutate(t, 2, tc.change)
			output, err := f.run(t, nil, "check-draft")
			f.assertFailure(t, output, err, 0)
			assert.Len(t, f.calls(t), 3)
		})
	}
}

func TestPublishReleaseRejectsAmbiguousDraftListing(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "title-collision", "tag-collision", "wrong-title", "wrong-tag"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublishReleaseFixture(t)
			var pages [][]map[string]any
			require.NoError(t, json.Unmarshal([]byte(f.steps[1].Response), &pages))
			target := pages[1][0]
			switch mode {
			case "missing":
				pages = pages[:1]
			case "wrong-title":
				target["name"] = "other title"
			case "wrong-tag":
				target["tag_name"] = "v0.1.1"
			default:
				collision := maps.Clone(target)
				collision["id"] = 43
				if mode == "title-collision" {
					collision["tag_name"] = "v0.1.1"
				}
				if mode == "tag-collision" {
					collision["name"] = "other title"
				}
				pages[1] = append(pages[1], collision)
			}
			f.steps[1].Response = publishReleaseJSON(t, pages)
			output, err := f.run(t, nil)
			f.assertFailure(t, output, err, 0)
			assert.Len(t, f.calls(t), 2, "ambiguous name/tag evidence must never reach staging or publication")
		})
	}
}

func TestPublishReleaseRejectsRecreatedBoundIdentity(t *testing.T) {
	for _, mode := range []string{"publish", "check-draft"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublishReleaseFixture(t)
			var args []string
			if mode == "check-draft" {
				f.asDraftCheck()
				args = []string{mode}
			}
			output, err := f.run(t, map[string]string{"RELEASE_ID": "43"}, args...)
			f.assertFailure(t, output, err, 0)
			assert.Len(t, f.calls(t), 2)
		})
	}
}

func TestPublishReleaseRejectsReleaseEvidence(t *testing.T) {
	for _, stage := range []struct {
		name, label   string
		index, writes int
	}{
		{name: "before", label: "draft", index: 2},
		{name: "response", label: "publication response", index: 3, writes: 1},
		{name: "after", label: "published", index: 4, writes: 1},
	} {
		for _, tc := range publishReleaseInvalidEvidence() {
			t.Run(stage.name+"-"+tc.name, func(t *testing.T) {
				f := newPublishReleaseFixture(t)
				f.mutate(t, stage.index, tc.change)
				output, err := f.run(t, nil)
				f.assertFailure(t, output, err, stage.writes)
				assert.Len(t, f.calls(t), stage.index+1, "reject invalid %s evidence immediately", stage.label)
			})
		}
	}
}

type publishReleaseMutation struct {
	name   string
	change func(*testing.T, map[string]any)
}

func publishReleaseInvalidEvidence() []publishReleaseMutation {
	cases := make([]publishReleaseMutation, 0, 34)
	cases = append(cases, []publishReleaseMutation{
		{"wrong-title", func(_ *testing.T, r map[string]any) { r["name"] = "another release" }},
		{"missing-title", func(_ *testing.T, r map[string]any) { delete(r, "name") }},
		{"wrong-tag", func(_ *testing.T, r map[string]any) { r["tag_name"] = "v0.1.1" }},
		{"wrong-prerelease", func(_ *testing.T, r map[string]any) { r["prerelease"] = true }},
		{"missing-prerelease", func(_ *testing.T, r map[string]any) { delete(r, "prerelease") }},
		{"string-prerelease", func(_ *testing.T, r map[string]any) { r["prerelease"] = "false" }},
		{"missing-id", func(_ *testing.T, r map[string]any) { delete(r, "id") }},
		{"zero-id", func(_ *testing.T, r map[string]any) { r["id"] = 0 }},
		{"negative-id", func(_ *testing.T, r map[string]any) { r["id"] = -1 }},
		{"fractional-id", func(_ *testing.T, r map[string]any) { r["id"] = 1.5 }},
		{"string-id", func(_ *testing.T, r map[string]any) { r["id"] = "42" }},
		{"unsafe-id", func(_ *testing.T, r map[string]any) { r["id"] = 9007199254740992 }},
		{"empty-notes", func(_ *testing.T, r map[string]any) { r["body"] = "" }},
		{"whitespace-notes", func(_ *testing.T, r map[string]any) { r["body"] = " \t\r\n" }},
		{"null-notes", func(_ *testing.T, r map[string]any) { r["body"] = nil }},
		{"missing-draft", func(_ *testing.T, r map[string]any) { delete(r, "draft") }},
		{"string-draft", func(_ *testing.T, r map[string]any) { r["draft"] = "true" }},
		{"missing-immutable", func(_ *testing.T, r map[string]any) { delete(r, "immutable") }},
		{"string-immutable", func(_ *testing.T, r map[string]any) { r["immutable"] = "false" }},
		{"missing-assets", func(_ *testing.T, r map[string]any) { delete(r, "assets") }},
		{"empty-assets", func(_ *testing.T, r map[string]any) { r["assets"] = []any{} }},
		{"missing-asset", func(t *testing.T, r map[string]any) { r["assets"] = publishReleaseAssets(t, r)[:7] }},
		{"extra-asset", func(t *testing.T, r map[string]any) {
			r["assets"] = append(publishReleaseAssets(t, r), map[string]any{"name": "source.zip", "state": "uploaded", "size": 1})
		}},
		{"duplicate-asset", func(t *testing.T, r map[string]any) {
			assets := publishReleaseAssets(t, r)
			assets[0] = assets[1]
		}},
		{"duplicate-asset-id", func(t *testing.T, r map[string]any) {
			assets := publishReleaseAssets(t, r)
			publishReleaseObject(t, assets[0])["id"] = publishReleaseObject(t, assets[1])["id"]
		}},
	}...)
	for key, value := range map[string]any{
		"id": 0, "name": "incorrect.tar.gz", "state": "starter", "size": 0,
	} {
		cases = append(cases, publishReleaseMutation{"asset-" + key, func(t *testing.T, r map[string]any) {
			publishReleaseObject(t, publishReleaseAssets(t, r)[0])[key] = value
		}})
	}
	for _, size := range []any{-1, 0.5, "1024", nil} {
		cases = append(cases, publishReleaseMutation{fmt.Sprintf("asset-size-%v", size), func(t *testing.T, r map[string]any) {
			publishReleaseObject(t, publishReleaseAssets(t, r)[0])["size"] = size
		}})
	}
	return cases
}

func TestPublishReleaseRejectsStateAndChangedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		index  int
		writes int
		change func(*testing.T, map[string]any)
	}{
		{"immutable-draft", 2, 0, func(_ *testing.T, r map[string]any) { r["immutable"] = true }},
		{"mutable-published", 2, 0, func(_ *testing.T, r map[string]any) { r["draft"] = false }},
		{"still-draft", 4, 1, func(_ *testing.T, r map[string]any) { r["draft"] = true }},
		{"not-immutable", 4, 1, func(_ *testing.T, r map[string]any) { r["immutable"] = false }},
		{"different-id", 4, 1, func(_ *testing.T, r map[string]any) { r["id"] = 43 }},
		{"changed-notes", 4, 1, func(_ *testing.T, r map[string]any) { r["body"] = "different notes" }},
		{"missing-published-at", 4, 1, func(_ *testing.T, r map[string]any) { delete(r, "published_at") }},
		{"empty-published-at", 4, 1, func(_ *testing.T, r map[string]any) { r["published_at"] = "" }},
		{"replaced-asset", 4, 1, func(t *testing.T, r map[string]any) {
			publishReleaseObject(t, publishReleaseAssets(t, r)[0])["id"] = 999
		}},
		{"changed-asset-size", 4, 1, func(t *testing.T, r map[string]any) {
			publishReleaseObject(t, publishReleaseAssets(t, r)[0])["size"] = 999
		}},
		{"changed-asset-digest", 4, 1, func(t *testing.T, r map[string]any) {
			publishReleaseObject(t, publishReleaseAssets(t, r)[0])["digest"] = "sha256:" + strings.Repeat("f", 64)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPublishReleaseFixture(t)
			f.mutate(t, tc.index, tc.change)
			output, err := f.run(t, nil)
			f.assertFailure(t, output, err, tc.writes)
		})
	}
}

func TestPublishReleaseRejectsWrongTagCommit(t *testing.T) {
	for _, index := range []int{0, 5} {
		for _, sha := range []any{strings.Repeat("f", 40), strings.ToUpper(publishReleaseSHA), "01234567", nil} {
			t.Run(fmt.Sprintf("call-%d-sha-%v", index, sha), func(t *testing.T) {
				f := newPublishReleaseFixture(t)
				f.mutate(t, index, func(_ *testing.T, r map[string]any) { r["sha"] = sha })
				output, err := f.run(t, nil)
				f.assertFailure(t, output, err, index/5)
				assert.Len(t, f.calls(t), index+1)
			})
		}
	}
}

func TestPublishReleaseRejectsForeignURLs(t *testing.T) {
	for _, url := range []any{
		"https://github.com.evil.example/" + publishReleaseRepository + "/releases/tag/v0.1.0",
		"https://github.com/another/repository/releases/tag/v0.1.0",
		"https://github.com/" + publishReleaseRepository + "/releases/tag/v0.1.1",
		"https://github.com/" + publishReleaseRepository + "/releases/tag/v0.1.0?extra=value",
		"https://github.com/" + publishReleaseRepository + "/releases/tag/v0.1.0\nother=value",
		nil,
	} {
		t.Run(fmt.Sprint(url), func(t *testing.T) {
			f := newPublishReleaseFixture(t)
			f.mutate(t, 4, func(_ *testing.T, r map[string]any) { r["html_url"] = url })
			output, err := f.run(t, nil)
			f.assertFailure(t, output, err, 1)
		})
	}
}

func TestPublishReleaseRejectsMalformedJSONAndAPIFailures(t *testing.T) {
	for index := range 6 {
		for _, mode := range []string{"invalid", "truncated", "multiple", "array", "object", "null", "empty", "api-failure"} {
			t.Run(fmt.Sprintf("call-%d-%s", index, mode), func(t *testing.T) {
				f := newPublishReleaseFixture(t)
				f.steps[index].Response = map[string]string{
					"invalid": publishReleaseNotes + publishReleaseToken, "truncated": "{", "multiple": f.steps[index].Response + "\n{}",
					"array": "[]", "object": "{}", "null": "null", "empty": "", "api-failure": f.steps[index].Response,
				}[mode]
				f.steps[index].Fail = mode == "api-failure"
				output, err := f.run(t, nil)
				writes := 0
				if index >= 3 {
					writes = 1
				}
				f.assertFailure(t, output, err, writes)
				assert.Len(t, f.calls(t), index+1)
			})
		}
	}
}

func TestPublishReleaseRejectsInputsBeforeAPI(t *testing.T) {
	for key, values := range map[string][]string{
		"VERSION":           {"", "0.1.0", "v0.1.0\nother", "v0.1.0/other", "v01.1.0", "v0.1.0-alpha..1", "v0.1.0-alpha.01"},
		"PRERELEASE":        {"", "TRUE", "true", "false\n"},
		"GITHUB_SHA":        {"", "01234567", strings.ToUpper(publishReleaseSHA), publishReleaseSHA + "\n"},
		"GITHUB_REPOSITORY": {"", "owner/repo/other", "owner/..", "owner/repo\nother", "owner/repo?query"},
		"GH_TOKEN":          {"", "token\nother", " token"},
		"RELEASE_ID":        {"", "0", "-1", "042", "42\n", "9007199254740992"},
		"GITHUB_OUTPUT":     {"", "/dev/null"},
		"GITHUB_SERVER_URL": {"", "http://github.com", "https://user@github.com", "https://github.com/", "https://github.com?query"},
	} {
		for _, value := range values {
			t.Run(key+"-"+value, func(t *testing.T) {
				f := newPublishReleaseFixture(t)
				output, err := f.run(t, map[string]string{key: value})
				f.assertFailure(t, output, err, 0)
				assert.Empty(t, f.calls(t))
			})
		}
	}
}

func TestPublishReleaseRejectsInvalidOutputAndArguments(t *testing.T) {
	for _, mode := range []string{"missing", "directory", "symlink", "read-only", "argument", "prerelease-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublishReleaseFixture(t)
			env := map[string]string{}
			var args []string
			switch mode {
			case "missing":
				env["GITHUB_OUTPUT"] = f.path("missing-output")
			case "directory":
				env["GITHUB_OUTPUT"] = f.root
			case "symlink":
				env["GITHUB_OUTPUT"] = f.path("linked-output")
				require.NoError(t, os.Symlink(f.path("output"), env["GITHUB_OUTPUT"]))
			case "read-only":
				if os.Geteuid() == 0 {
					t.Skip("root can write mode-0400 files")
				}
				require.NoError(t, os.Chmod(f.path("output"), 0o400))
			case "argument":
				args = []string{"unexpected"}
			case "prerelease-mismatch":
				env["VERSION"] = "v0.1.0-rc.1"
			}
			output, err := f.run(t, env, args...)
			f.assertFailure(t, output, err, 0)
			assert.Empty(t, f.calls(t))
		})
	}
}

// This isolated subprocess is the entire fake gh implementation. It consumes a
// sequence of exact API requests and fixtures; it has no network, authentication,
// tag, settings, merge or real GitHub CLI implementation.
func TestPublishReleaseFakeGH(t *testing.T) {
	if os.Getenv("PUBLISH_RELEASE_FAKE_GH") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		fmt.Fprintln(os.Stderr, "fake gh: missing command separator")
		os.Exit(1)
	}
	if err := publishReleaseFakeGH(os.Getenv("PUBLISH_RELEASE_TEST_ROOT"), os.Args[separator+1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fake gh:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

type publishReleaseStep struct {
	Args     []string `json:"args"`
	Input    string   `json:"input"`
	Response string   `json:"response"`
	Fail     bool     `json:"fail"`
}

type publishReleaseCall struct {
	Args  []string `json:"args"`
	Input string   `json:"input"`
}

type publishReleaseFixture struct {
	root, jq string
	env      map[string]string
	steps    []publishReleaseStep
}

func newPublishReleaseFixture(t *testing.T) *publishReleaseFixture {
	t.Helper()
	jq, err := exec.LookPath("jq")
	require.NoError(t, err, "publication tests require local jq")
	f := &publishReleaseFixture{root: t.TempDir(), jq: jq}
	require.NoError(t, os.Mkdir(f.path("bin"), 0o700))
	script, err := os.ReadFile("publish-release.sh")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.path("publish-release.sh"), script, 0o600))
	executable, err := os.Executable()
	require.NoError(t, err)
	shim := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestPublishReleaseFakeGH$ -- \"$@\"\n"
	require.NoError(t, os.WriteFile(f.path("bin", "gh"), []byte(shim), 0o700))
	require.NoError(t, os.WriteFile(f.path("output"), []byte(publishReleaseOutput), 0o600))
	f.prepare(t, "v0.1.0", "https://github.com", false)
	return f
}

func (f *publishReleaseFixture) asDraftCheck() {
	f.steps = []publishReleaseStep{f.steps[0], f.steps[1], f.steps[2], f.steps[len(f.steps)-1]}
}

func (f *publishReleaseFixture) path(parts ...string) string {
	return filepath.Join(append([]string{f.root}, parts...)...)
}

func (f *publishReleaseFixture) releaseURL() string {
	return f.env["GITHUB_SERVER_URL"] + "/" + publishReleaseRepository + "/releases/tag/" + f.env["VERSION"]
}

func (f *publishReleaseFixture) prepare(t *testing.T, version, server string, retry bool) {
	t.Helper()
	prerelease := strings.Contains(version, "-")
	f.env = map[string]string{
		"PATH":    f.path("bin") + string(os.PathListSeparator) + filepath.Dir(f.jq) + ":/usr/bin:/bin",
		"VERSION": version, "PRERELEASE": fmt.Sprint(prerelease), "GITHUB_SHA": publishReleaseSHA,
		"GITHUB_REPOSITORY": publishReleaseRepository, "GH_TOKEN": publishReleaseToken,
		"RELEASE_ID": "42", "GITHUB_OUTPUT": f.path("output"), "GITHUB_SERVER_URL": server,
		"GORACE": "atexit_sleep_ms=0", "PUBLISH_RELEASE_FAKE_GH": "1", "PUBLISH_RELEASE_TEST_ROOT": f.root,
		"GH_DEBUG": "api",
	}
	release := map[string]any{
		"id":               42,
		"tag_name":         version,
		"target_commitish": "main",
		"name":             version,
		"body":             publishReleaseNotes,
		"published_at":     "2026-10-27T15:00:00Z",
		"draft":            false,
		"prerelease":       prerelease,
		"immutable":        true,
		"html_url":         f.releaseURL(),
		"assets": publishReleaseDefaultAssets(
			version,
		),
		"zipball_url": "https://example.org/source.zip",
		"tarball_url": "https://example.org/source.tar.gz",
	}
	published := publishReleaseJSON(t, release)
	release["draft"], release["immutable"] = !retry, retry
	if !retry {
		release["html_url"] = server + "/" + publishReleaseRepository + "/releases/tag/untagged-draft"
		release["published_at"] = nil
	}
	base := "repos/" + publishReleaseRepository
	commit := publishReleaseJSON(t, map[string]any{"sha": publishReleaseSHA})
	pages := [][]any{{map[string]any{"id": 1, "name": "v0.0.0", "tag_name": "v0.0.0"}}, {release}}
	listing := f.step("GET", base+"/releases?per_page=100", publishReleaseJSON(t, pages))
	listing.Args = append(listing.Args, "--paginate", "--slurp")
	f.steps = []publishReleaseStep{
		f.step("GET", base+"/commits/"+version, commit),
		listing,
		f.step("GET", base+"/releases/42", publishReleaseJSON(t, release)),
	}
	if !retry {
		patch := f.step("PATCH", base+"/releases/42", published)
		patch.Args = append(patch.Args, "--input", "-")
		patch.Input = fmt.Sprintf("{\"draft\":false,\"make_latest\":\"%t\"}\n", !prerelease)
		f.steps = append(f.steps, patch)
	}
	f.steps = append(f.steps, f.step("GET", base+"/releases/42", published), f.step("GET", base+"/commits/"+version, commit))
}

func (f *publishReleaseFixture) step(method, endpoint, response string) publishReleaseStep {
	return publishReleaseStep{Args: []string{
		"api", "--hostname", strings.TrimPrefix(f.env["GITHUB_SERVER_URL"], "https://"),
		"--header", "Accept: application/vnd.github+json", "--header", "X-GitHub-Api-Version: 2026-03-10",
		"--method", method, endpoint,
	}, Response: response}
}

func (f *publishReleaseFixture) mutate(t *testing.T, index int, change func(*testing.T, map[string]any)) {
	t.Helper()
	var release map[string]any
	require.NoError(t, json.Unmarshal([]byte(f.steps[index].Response), &release))
	change(t, release)
	f.steps[index].Response = publishReleaseJSON(t, release)
}

func (f *publishReleaseFixture) run(t *testing.T, overrides map[string]string, args ...string) ([]byte, error) {
	t.Helper()
	require.NoError(t, os.WriteFile(f.path("steps.json"), []byte(publishReleaseJSON(t, f.steps)), 0o600))
	// A fresh environment excludes all workstation credentials and config. The
	// shim is first on PATH; /bin/bash also exercises Bash 3.2 on macOS.
	cmd := exec.CommandContext(t.Context(), "/bin/bash", append([]string{f.path("publish-release.sh")}, args...)...)
	cmd.Dir = f.root
	env := maps.Clone(f.env)
	maps.Copy(env, overrides)
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	for i, call := range f.calls(t) {
		require.Less(t, i, len(f.steps), "no unplanned gh operations are allowed")
		assert.Equal(t, f.steps[i].Args, call.Args)
		assert.Equal(t, f.steps[i].Input, call.Input)
	}
	if err != nil {
		return output, fmt.Errorf("running final publication helper: %w", err)
	}
	return output, nil
}

func (f *publishReleaseFixture) calls(t *testing.T) []publishReleaseCall {
	t.Helper()
	calls, err := publishReleaseReadCalls(f.root)
	require.NoError(t, err)
	return calls
}

func (f *publishReleaseFixture) writes(t *testing.T) int {
	t.Helper()
	writes := 0
	for _, call := range f.calls(t) {
		if slices.Contains(call.Args, "PATCH") {
			writes++
		}
	}
	return writes
}

func (f *publishReleaseFixture) assertFailure(t *testing.T, output []byte, err error, writes int) {
	t.Helper()
	require.Error(t, err, "%s", output)
	assert.Contains(t, string(output), "publish-release:")
	assert.Less(t, len(output), 512, "diagnostics must remain bounded")
	assert.NotContains(t, string(output), publishReleaseToken)
	assert.NotContains(t, string(output), publishReleaseNotes)
	f.assertOutput(t, publishReleaseOutput)
	assert.Equal(t, writes, f.writes(t))
}

func (f *publishReleaseFixture) assertOutput(t *testing.T, expected string) {
	t.Helper()
	data, err := os.ReadFile(f.path("output"))
	require.NoError(t, err)
	assert.Equal(t, expected, string(data))
}

func publishReleaseJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return string(data)
}

func publishReleaseObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok, "expected a fixture object")
	return object
}

func publishReleaseAssets(t *testing.T, release map[string]any) []any {
	t.Helper()
	assets, ok := release["assets"].([]any)
	require.True(t, ok, "expected fixture assets")
	return assets
}

func publishReleaseDefaultAssets(version string) []any {
	version = strings.TrimPrefix(version, "v")
	project := "cluster-api-provider-composite"
	names := make([]string, 0, 8)
	names = append(names, "metadata.yaml", "checksums.txt", "infrastructure-components.yaml", project+"-"+version+".tgz")
	for _, platform := range []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"} {
		names = append(names, project+"_"+version+"_"+platform+".tar.gz")
	}
	assets := make([]any, 0, len(names))
	for i, name := range names {
		assets = append(assets, map[string]any{
			"id": i + 1, "name": name, "state": "uploaded", "size": 1024,
			"digest": "sha256:" + strings.Repeat("a", 64), "download_count": 0,
		})
	}
	return assets
}

func publishReleaseReadCalls(root string) ([]publishReleaseCall, error) {
	data, err := os.ReadFile(filepath.Join(root, "calls.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading fake gh calls: %w", err)
	}
	var calls []publishReleaseCall
	if err := json.Unmarshal(data, &calls); err != nil {
		return nil, fmt.Errorf("decoding fake gh calls: %w", err)
	}
	return calls, nil
}

func publishReleaseFakeGH(root string, args []string) error {
	data, err := os.ReadFile(filepath.Join(root, "steps.json"))
	if err != nil {
		return fmt.Errorf("reading fake gh steps: %w", err)
	}
	var steps []publishReleaseStep
	if err := json.Unmarshal(data, &steps); err != nil {
		return fmt.Errorf("decoding fake gh steps: %w", err)
	}
	call, index, err := publishReleaseRecordCall(root, args)
	if err != nil {
		return err
	}
	if index >= len(steps) {
		return errors.New("unexpected extra gh request")
	}
	return publishReleaseRespond(steps[index], call)
}

func publishReleaseRecordCall(root string, args []string) (publishReleaseCall, int, error) {
	call := publishReleaseCall{Args: args}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return call, 0, fmt.Errorf("reading fake gh input: %w", err)
	}
	call.Input = string(input)
	calls, err := publishReleaseReadCalls(root)
	if err != nil {
		return call, 0, err
	}
	index := len(calls)
	log, err := os.OpenFile(filepath.Join(root, "calls.json"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return call, 0, fmt.Errorf("opening fake gh call log: %w", err)
	}
	encodeErr := json.NewEncoder(log).Encode(append(calls, call))
	closeErr := log.Close()
	if err := errors.Join(encodeErr, closeErr); err != nil {
		return call, 0, fmt.Errorf("recording fake gh calls: %w", err)
	}
	return call, index, nil
}

func publishReleaseRespond(step publishReleaseStep, call publishReleaseCall) error {
	if !slices.Equal(step.Args, call.Args) || step.Input != call.Input {
		return fmt.Errorf("unexpected fake gh request: %v (input %q)", call.Args, call.Input)
	}
	if _, err := fmt.Fprint(os.Stdout, step.Response); err != nil {
		return fmt.Errorf("writing fake gh response: %w", err)
	}
	if step.Fail {
		return errors.New("injected API failure: " + os.Getenv("GH_TOKEN") + " " + publishReleaseNotes)
	}
	return nil
}
