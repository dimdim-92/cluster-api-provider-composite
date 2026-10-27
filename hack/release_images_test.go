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
	releaseImagesSHA        = "0123456789abcdef0123456789abcdef01234567"
	releaseImagesVersion    = "v0.1.0-alpha.2"
	releaseImagesRepository = "ExampleOwner/Composite-Operator"
	releaseImagesImage      = "ghcr.io/exampleowner/composite-operator"
	releaseImagesIndex      = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	releaseImagesOutput     = "existing=preserved\n"
)

func releaseImagesID(arch string) string {
	if arch == "amd64" {
		return "sha256:" + strings.Repeat("1", 64)
	}
	return "sha256:" + strings.Repeat("2", 64)
}

func releaseImagesDigest(arch string) string {
	if arch == "amd64" {
		return "sha256:" + strings.Repeat("a", 64)
	}
	return "sha256:" + strings.Repeat("b", 64)
}

func TestReleaseImagesVerify(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			fixture := newReleaseImagesFixture(t)
			output, err := fixture.run(t, nil, "verify", fixture.artifact(arch), arch)
			require.NoError(t, err, "%s", output)
			assert.Equal(t, [][]string{
				{"image", "load", "--input", filepath.Join(fixture.artifact(arch), "image.tar")},
				{"image", "inspect", "composite-ci:" + releaseImagesSHA + "-" + arch},
			}, fixture.calls(t))
			fixture.assertOutput(t, releaseImagesOutput)
		})
	}
	t.Run("metadata-without-final-newline", func(t *testing.T) {
		fixture := newReleaseImagesFixture(t)
		for name, value := range map[string]string{
			"commit.sha": releaseImagesSHA, "version": releaseImagesVersion, "image.id": releaseImagesID("amd64"),
		} {
			fixture.writeArtifact(t, "amd64", name, value)
		}
		output, err := fixture.run(t, nil, "verify", fixture.artifact("amd64"), "amd64")
		require.NoError(t, err, "%s", output)
	})
}

func TestReleaseImagesVerifyRejectsInvalidArtifacts(t *testing.T) {
	type artifactCase struct {
		name   string
		change func(*testing.T, *releaseImagesFixture)
	}
	cases := make([]artifactCase, 0, 25)
	cases = append(cases, []artifactCase{
		{name: "wrong-commit", change: func(t *testing.T, f *releaseImagesFixture) {
			f.writeArtifact(t, "amd64", "commit.sha", strings.Repeat("f", 40)+"\n")
		}},
		{name: "wrong-version", change: func(t *testing.T, f *releaseImagesFixture) {
			f.writeArtifact(t, "amd64", "version", "v0.1.0\n")
		}},
		{name: "extra-metadata-line", change: func(t *testing.T, f *releaseImagesFixture) {
			f.writeArtifact(t, "amd64", "commit.sha", releaseImagesSHA+"\n\n")
		}},
		{name: "metadata-trailing-space", change: func(t *testing.T, f *releaseImagesFixture) {
			f.writeArtifact(t, "amd64", "version", releaseImagesVersion+" \n")
		}},
		{name: "metadata-nul", change: func(t *testing.T, f *releaseImagesFixture) {
			f.writeArtifact(t, "amd64", "image.id", releaseImagesID("amd64")+"\x00\n")
		}},
		{name: "invalid-image-id", change: func(t *testing.T, f *releaseImagesFixture) {
			f.writeArtifact(t, "amd64", "image.id", "sha256:1234\n")
		}},
		{name: "uppercase-image-id", change: func(t *testing.T, f *releaseImagesFixture) {
			f.writeArtifact(t, "amd64", "image.id", "sha256:"+strings.Repeat("A", 64)+"\n")
		}},
		{name: "extra-image-id-line", change: func(t *testing.T, f *releaseImagesFixture) {
			f.writeArtifact(t, "amd64", "image.id", releaseImagesID("amd64")+"\n\n")
		}},
		{name: "wrong-loaded-id", change: func(t *testing.T, f *releaseImagesFixture) {
			f.mutateImage(t, "amd64", func(image map[string]any) { image["Id"] = releaseImagesID("arm64") })
		}},
		{name: "wrong-os", change: func(t *testing.T, f *releaseImagesFixture) {
			f.mutateImage(t, "amd64", func(image map[string]any) { image["Os"] = "windows" })
		}},
		{name: "wrong-arch", change: func(t *testing.T, f *releaseImagesFixture) {
			f.mutateImage(t, "amd64", func(image map[string]any) { image["Architecture"] = "arm64" })
		}},
		{name: "missing-labels", change: func(t *testing.T, f *releaseImagesFixture) {
			f.mutateImage(t, "amd64", func(image map[string]any) { image["Config"] = nil })
		}},
		{name: "stale-local-tag", change: func(t *testing.T, f *releaseImagesFixture) {
			f.config.LoadTags = map[string]string{"amd64": "unrelated:latest"}
			image := releaseImagesReadJSON[map[string]any](t, filepath.Join(f.artifact("amd64"), "image.tar"))
			releaseImagesWriteJSON(t, f.path("state.json"), releaseImagesFakeState{
				Images: map[string]map[string]any{"composite-ci:" + releaseImagesSHA + "-amd64": image},
			})
		}},
		{name: "load-failure", change: func(_ *testing.T, f *releaseImagesFixture) {
			f.config.FailPrefix = []string{"image", "load"}
		}},
		{name: "inspect-failure", change: func(_ *testing.T, f *releaseImagesFixture) {
			f.config.FailPrefix = []string{"image", "inspect"}
		}},
	}...)
	for _, name := range []string{"image.tar", "image.id", "commit.sha", "version"} {
		cases = append(cases, artifactCase{"missing-" + name, func(t *testing.T, f *releaseImagesFixture) {
			require.NoError(t, os.Remove(filepath.Join(f.artifact("amd64"), name)))
		}})
	}
	for _, label := range []string{"revision", "source", "version"} {
		for _, value := range []string{"", "incorrect"} {
			cases = append(cases, artifactCase{"label-" + label + "-" + value, func(t *testing.T, f *releaseImagesFixture) {
				f.mutateImage(t, "amd64", func(image map[string]any) {
					labels := releaseImagesMap(t, releaseImagesMap(t, image["Config"])["Labels"])
					labels["org.opencontainers.image."+label] = value
				})
			}})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newReleaseImagesFixture(t)
			tc.change(t, fixture)
			output, err := fixture.run(t, nil, "verify", fixture.artifact("amd64"), "amd64")
			require.Error(t, err, "%s", output)
			assert.Contains(t, string(output), "release-images:")
			fixture.assertNoPublication(t)
			fixture.assertOutput(t, releaseImagesOutput)
		})
	}
}

func TestReleaseImagesRejectsInvalidInputsBeforeDocker(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
	}{
		{name: "unsupported-arch", args: []string{"verify", "unused", "aarch64"}},
		{name: "no-command"},
		{name: "unknown-command", args: []string{"build"}},
		{name: "verify-missing-arch", args: []string{"verify", "unused"}},
		{name: "publish-extra-argument", args: []string{"publish", "unused", "extra"}},
		{name: "missing-version", env: map[string]string{"VERSION": ""}},
		{name: "invalid-version", env: map[string]string{"VERSION": "v0.1.0\nlatest"}},
		{name: "missing-sha", env: map[string]string{"GITHUB_SHA": ""}},
		{name: "short-sha", env: map[string]string{"GITHUB_SHA": "01234567"}},
		{name: "invalid-repository", env: map[string]string{"GITHUB_REPOSITORY": "owner/repo/other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newReleaseImagesFixture(t)
			args := tc.args
			if tc.env != nil {
				args = []string{"verify", fixture.artifact("amd64"), "amd64"}
			}
			output, err := fixture.run(t, tc.env, args...)
			require.Error(t, err, "%s", output)
			assert.Empty(t, fixture.calls(t))
			fixture.assertOutput(t, releaseImagesOutput)
		})
	}
}

func TestReleaseImagesPublish(t *testing.T) {
	fixture := newReleaseImagesFixture(t)
	output, err := fixture.run(t, nil, "publish", fixture.path("image artifacts"))
	require.NoError(t, err, "%s", output)
	fixture.assertOutput(t, releaseImagesOutput+"digest="+releaseImagesIndex+"\n")
	calls := fixture.calls(t)
	var loads, pushes, creates, inspectedAliases int
	for _, call := range calls {
		switch {
		case slices.Equal(call[:2], []string{"image", "load"}):
			loads++
		case slices.Equal(call[:2], []string{"image", "tag"}):
			require.Equal(t, 2, loads, "validate/load BOTH images before publication")
			arch := "amd64"
			if strings.HasSuffix(call[3], "-arm64") {
				arch = "arm64"
			}
			assert.Equal(t, releaseImagesID(arch), call[2], "tag the unchanged validated image ID")
			assert.Equal(t, releaseImagesImage+":sha-"+releaseImagesSHA+"-"+arch, call[3])
		case slices.Equal(call[:2], []string{"image", "push"}):
			require.Equal(t, 2, loads)
			pushes++
		case slices.Equal(call[:3], []string{"buildx", "imagetools", "create"}):
			assert.Equal(t, 2, pushes)
			creates++
			assert.Equal(t, []string{
				"buildx", "imagetools", "create",
				"--tag", releaseImagesImage + ":" + releaseImagesVersion,
				"--tag", releaseImagesImage + ":v0.1",
				"--tag", releaseImagesImage + ":latest",
				releaseImagesImage + "@" + releaseImagesDigest("amd64"),
				releaseImagesImage + "@" + releaseImagesDigest("arm64"),
			}, call)
		case slices.Equal(call[:3], []string{"buildx", "imagetools", "inspect"}):
			assert.Equal(t, []string{"--format", "{{json .Manifest}}"}, call[3:5])
			inspectedAliases++
		}
	}
	assert.Equal(t, 2, loads)
	assert.Equal(t, 2, pushes)
	assert.Equal(t, 1, creates, "create only one multiarch index, with all aliases")
	assert.Equal(t, 3, inspectedAliases, "check every published alias before output")
}

func TestReleaseImagesPublishValidatesBothBeforeAnyPush(t *testing.T) {
	for _, name := range []string{"missing-arm64", "wrong-arm64-metadata", "wrong-arm64-id", "wrong-arm64-label", "arm64-load-fails"} {
		t.Run(name, func(t *testing.T) {
			fixture := newReleaseImagesFixture(t)
			switch name {
			case "missing-arm64":
				for _, name := range []string{"image.tar", "image.id", "commit.sha", "version"} {
					require.NoError(t, os.Remove(filepath.Join(fixture.artifact("arm64"), name)))
				}
				require.NoError(t, os.Remove(fixture.artifact("arm64")))
			case "wrong-arm64-metadata":
				fixture.writeArtifact(t, "arm64", "version", "v0.1.0\n")
			case "wrong-arm64-id":
				fixture.mutateImage(t, "arm64", func(image map[string]any) { image["Id"] = releaseImagesID("amd64") })
			case "wrong-arm64-label":
				fixture.mutateImage(t, "arm64", func(image map[string]any) { image["Config"] = nil })
			case "arm64-load-fails":
				fixture.config.FailPrefix = []string{"image", "load", "--input", filepath.Join(fixture.artifact("arm64"), "image.tar")}
			}
			output, err := fixture.run(t, nil, "publish", fixture.path("image artifacts"))
			require.Error(t, err, "%s", output)
			fixture.assertNoPublication(t)
			fixture.assertOutput(t, releaseImagesOutput)
		})
	}
}

func TestReleaseImagesPublishRejectsUnsafeAliasesBeforeDocker(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{name: "different-image", env: map[string]string{"IMAGE": "ghcr.io/other/repository"}},
		{name: "tagged-image", env: map[string]string{"IMAGE": releaseImagesImage + ":latest"}},
		{name: "uppercase-image", env: map[string]string{"IMAGE": "ghcr.io/ExampleOwner/Composite-Operator"}},
		{name: "foreign-repository", env: map[string]string{"IMAGE_TAGS": "ghcr.io/other/repository:latest"}},
		{name: "repository-prefix", env: map[string]string{"IMAGE_TAGS": releaseImagesImage + "-evil:latest"}},
		{name: "missing-version", env: map[string]string{"IMAGE_TAGS": releaseImagesImage + ":latest"}},
		{name: "missing-v-prefix", env: map[string]string{"IMAGE_TAGS": releaseImagesImage + ":0.1.0-alpha.2"}},
		{name: "empty-tags", env: map[string]string{"IMAGE_TAGS": ""}},
		{name: "empty-tag", env: map[string]string{"IMAGE_TAGS": releaseImagesImage + ":"}},
		{name: "digest-alias", env: map[string]string{"IMAGE_TAGS": releaseImagesImage + "@" + releaseImagesDigest("amd64")}},
		{name: "whitespace-tag", env: map[string]string{"IMAGE_TAGS": releaseImagesImage + ":latest "}},
		{name: "carriage-return", env: map[string]string{"IMAGE_TAGS": releaseImagesImage + ":latest\r"}},
		{name: "too-long-tag", env: map[string]string{"IMAGE_TAGS": releaseImagesImage + ":" + strings.Repeat("a", 129)}},
		{name: "platform-alias", env: map[string]string{"IMAGE_TAGS": releaseImagesImage + ":sha-" + releaseImagesSHA + "-amd64"}},
		{name: "missing-output", env: map[string]string{"GITHUB_OUTPUT": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newReleaseImagesFixture(t)
			// Invalid aliases must be rejected even alongside the required version.
			if tags, found := tc.env["IMAGE_TAGS"]; found && tc.name != "missing-version" && tc.name != "missing-v-prefix" && tags != "" {
				tc.env["IMAGE_TAGS"] = releaseImagesImage + ":" + releaseImagesVersion + "\n" + tags
			}
			output, err := fixture.run(t, tc.env, "publish", fixture.path("image artifacts"))
			require.Error(t, err, "%s", output)
			assert.Empty(t, fixture.calls(t))
			fixture.assertOutput(t, releaseImagesOutput)
		})
	}
}

func TestReleaseImagesPublishRejectsInvalidRepositoryDigests(t *testing.T) {
	for _, tc := range []struct {
		name    string
		digests []string
	}{
		{name: "missing", digests: []string{}},
		{name: "another-repository", digests: []string{"ghcr.io/other/operator@" + releaseImagesDigest("amd64")}},
		{name: "repository-prefix", digests: []string{releaseImagesImage + "-evil@" + releaseImagesDigest("amd64")}},
		{name: "repository-subpath", digests: []string{releaseImagesImage + "/evil@" + releaseImagesDigest("amd64")}},
		{name: "malformed", digests: []string{releaseImagesImage + "@sha256:1234"}},
		{name: "trailing-newline", digests: []string{releaseImagesImage + "@" + releaseImagesDigest("amd64") + "\n"}},
		{name: "wrong-algorithm", digests: []string{releaseImagesImage + "@sha512:" + strings.Repeat("a", 128)}},
		{name: "ambiguous", digests: []string{releaseImagesImage + "@" + releaseImagesDigest("amd64"), releaseImagesImage + "@" + releaseImagesDigest("arm64")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newReleaseImagesFixture(t)
			fixture.config.RepoDigests = map[string][]string{"amd64": tc.digests}
			output, err := fixture.run(t, nil, "publish", fixture.path("image artifacts"))
			require.Error(t, err, "%s", output)
			assert.Contains(t, string(output), "digest")
			for _, call := range fixture.calls(t) {
				assert.NotEqual(t, "buildx", call[0], "invalid repo digests must block index creation")
			}
			fixture.assertOutput(t, releaseImagesOutput)
		})
	}
}

func TestReleaseImagesPublishFailuresLeaveOutputUntouched(t *testing.T) {
	for _, prefix := range [][]string{
		{"image", "tag"},
		{"image", "push"},
		{"image", "push", releaseImagesImage + ":sha-" + releaseImagesSHA + "-arm64"},
		{"buildx", "imagetools", "create"},
		{"buildx", "imagetools", "inspect"},
	} {
		t.Run(strings.Join(prefix, "-"), func(t *testing.T) {
			fixture := newReleaseImagesFixture(t)
			fixture.config.FailPrefix = prefix
			output, err := fixture.run(t, nil, "publish", fixture.path("image artifacts"))
			require.Error(t, err, "%s", output)
			calls := fixture.calls(t)
			require.NotEmpty(t, calls)
			assert.Equal(t, prefix, calls[len(calls)-1][:len(prefix)], "stop immediately on Docker failure")
			fixture.assertOutput(t, releaseImagesOutput)
		})
	}
}

func TestReleaseImagesPublishValidatesIndexAndEveryAlias(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "no-index-digest", change: func(index map[string]any) { delete(index, "digest") }},
		{name: "short-index-digest", change: func(index map[string]any) { index["digest"] = "sha256:abc" }},
		{name: "uppercase-index-digest", change: func(index map[string]any) { index["digest"] = "sha256:" + strings.Repeat("C", 64) }},
		{name: "digest-line-injection", change: func(index map[string]any) { index["digest"] = releaseImagesIndex + "\nother=value" }},
		{name: "digest-trailing-newline", change: func(index map[string]any) { index["digest"] = releaseImagesIndex + "\n" }},
		{name: "not-an-index", change: func(index map[string]any) { index["mediaType"] = "application/vnd.oci.image.manifest.v1+json" }},
		{name: "wrong-schema", change: func(index map[string]any) { index["schemaVersion"] = 1 }},
		{name: "missing-platform", change: func(index map[string]any) { index["manifests"] = releaseImagesManifests(t, index)[:1] }},
		{name: "extra-platform", change: func(index map[string]any) {
			index["manifests"] = append(releaseImagesManifests(t, index), map[string]any{"platform": map[string]any{"os": "linux", "architecture": "s390x"}})
		}},
		{name: "wrong-platform-os", change: func(index map[string]any) {
			releaseImagesMap(t, releaseImagesMap(t, releaseImagesManifests(t, index)[0])["platform"])["os"] = "windows"
		}},
		{name: "wrong-platform-architecture", change: func(index map[string]any) {
			releaseImagesMap(t, releaseImagesMap(t, releaseImagesManifests(t, index)[0])["platform"])["architecture"] = "amd64"
		}},
		{name: "wrong-platform-digest", change: func(index map[string]any) {
			releaseImagesMap(t, releaseImagesManifests(t, index)[0])["digest"] = releaseImagesDigest("amd64")
		}},
		{name: "nested-index-descriptor", change: func(index map[string]any) {
			releaseImagesMap(t, releaseImagesManifests(t, index)[0])["mediaType"] = "application/vnd.oci.image.index.v1+json"
		}},
		{name: "different-alias-digest", change: func(index map[string]any) { index["digest"] = "sha256:" + strings.Repeat("d", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newReleaseImagesFixture(t)
			index := releaseImagesDefaultIndex()
			tc.change(index)
			data, err := json.Marshal(index)
			require.NoError(t, err)
			// Keep the version alias valid; a later failing alias must still block output.
			fixture.config.Indexes = map[string]json.RawMessage{releaseImagesImage + ":latest": data}
			output, err := fixture.run(t, nil, "publish", fixture.path("image artifacts"))
			require.Error(t, err, "%s", output)
			fixture.assertOutput(t, releaseImagesOutput)
		})
	}
	t.Run("docker-manifest-list", func(t *testing.T) {
		fixture := newReleaseImagesFixture(t)
		index := releaseImagesDefaultIndex()
		index["mediaType"] = "application/vnd.docker.distribution.manifest.list.v2+json"
		for _, manifest := range releaseImagesManifests(t, index) {
			releaseImagesMap(t, manifest)["mediaType"] = "application/vnd.docker.distribution.manifest.v2+json"
		}
		data, err := json.Marshal(index)
		require.NoError(t, err)
		fixture.config.Index = data
		output, err := fixture.run(t, nil, "publish", fixture.path("image artifacts"))
		require.NoError(t, err, "%s", output)
		fixture.assertOutput(t, releaseImagesOutput+"digest="+releaseImagesIndex+"\n")
	})
}

func TestReleaseImagesRejectsMalformedDockerJSON(t *testing.T) {
	for _, mode := range []string{"verify", "publish"} {
		for _, malformed := range []string{"invalid-json", "truncated-json", "multiple-documents", "wrong-shape"} {
			t.Run(mode+"-"+malformed, func(t *testing.T) {
				fixture := newReleaseImagesFixture(t)
				var valid []byte
				if mode == "publish" {
					var err error
					valid, err = json.Marshal(releaseImagesDefaultIndex())
					require.NoError(t, err)
				} else {
					image := releaseImagesReadJSON[map[string]any](t, filepath.Join(fixture.artifact("amd64"), "image.tar"))
					var err error
					valid, err = json.Marshal([]map[string]any{image})
					require.NoError(t, err)
				}
				value := "invalid JSON"
				switch malformed {
				case "truncated-json":
					value = "["
				case "multiple-documents":
					value = string(valid) + "\n" + string(valid)
				case "wrong-shape":
					value = `"unexpected string"`
				}
				args := []string{"verify", fixture.artifact("amd64"), "amd64"}
				if mode == "publish" {
					fixture.config.IndexOutput = value
					args = []string{"publish", fixture.path("image artifacts")}
				} else {
					fixture.config.InspectOutput = value
				}
				output, err := fixture.run(t, nil, args...)
				require.Error(t, err, "%s", output)
				fixture.assertOutput(t, releaseImagesOutput)
			})
		}
	}
}

func TestReleaseImagesRejectsSymlinksBeforeDocker(t *testing.T) {
	for _, name := range []string{"artifact-directory", "metadata-file", "github-output"} {
		t.Run(name, func(t *testing.T) {
			fixture := newReleaseImagesFixture(t)
			args := []string{"verify", fixture.artifact("amd64"), "amd64"}
			overrides := map[string]string{}
			switch name {
			case "artifact-directory":
				link := fixture.path("linked-artifact")
				require.NoError(t, os.Symlink(fixture.artifact("amd64"), link))
				args[1] = link
			case "metadata-file":
				path := filepath.Join(fixture.artifact("amd64"), "version")
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.WriteFile(fixture.path("version"), []byte(releaseImagesVersion), 0o600))
				require.NoError(t, os.Symlink(fixture.path("version"), path))
			case "github-output":
				link := fixture.path("linked-output")
				require.NoError(t, os.Symlink(fixture.path("output"), link))
				overrides["GITHUB_OUTPUT"] = link
				args = []string{"publish", fixture.path("image artifacts")}
			}
			output, err := fixture.run(t, overrides, args...)
			require.Error(t, err, "%s", output)
			assert.Empty(t, fixture.calls(t))
			fixture.assertOutput(t, releaseImagesOutput)
		})
	}
}

// The fake Docker is this test executable, re-executed behind a PATH shim. It has
// no daemon, registry, authentication or network implementation. Unknown commands
// (including all builds) fail, so no test can accidentally invoke real Docker.
func TestReleaseImagesFakeDocker(t *testing.T) {
	if os.Getenv("RELEASE_IMAGES_FAKE_DOCKER") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		fmt.Fprintln(os.Stderr, "fake docker: missing command separator")
		os.Exit(1)
	}
	if err := releaseImagesFakeDocker(os.Getenv("RELEASE_IMAGES_TEST_ROOT"), os.Args[separator+1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fake docker:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

type releaseImagesFakeConfig struct {
	FailPrefix    []string                   `json:"failPrefix,omitempty"`
	LoadTags      map[string]string          `json:"loadTags,omitempty"`
	RepoDigests   map[string][]string        `json:"repoDigests,omitempty"`
	InspectOutput string                     `json:"inspectOutput,omitempty"`
	IndexOutput   string                     `json:"indexOutput,omitempty"`
	Index         json.RawMessage            `json:"index,omitempty"`
	Indexes       map[string]json.RawMessage `json:"indexes,omitempty"`
}

type releaseImagesFakeState struct {
	Images  map[string]map[string]any `json:"images"`
	Pushed  []string                  `json:"pushed"`
	Aliases []string                  `json:"aliases"`
}

type releaseImagesFixture struct {
	root   string
	jq     string
	config releaseImagesFakeConfig
}

func newReleaseImagesFixture(t *testing.T) *releaseImagesFixture {
	t.Helper()
	jq, err := exec.LookPath("jq")
	require.NoError(t, err, "release image helper tests require the local jq binary")
	f := &releaseImagesFixture{root: t.TempDir(), jq: jq}
	require.NoError(t, os.Mkdir(f.path("bin"), 0o700))
	script, err := os.ReadFile("release-images.sh")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.path("release-images.sh"), script, 0o600))
	executable, err := os.Executable()
	require.NoError(t, err)
	shim := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestReleaseImagesFakeDocker$ -- \"$@\"\n"
	require.NoError(t, os.WriteFile(f.path("bin", "docker"), []byte(shim), 0o700))
	require.NoError(t, os.WriteFile(f.path("output"), []byte(releaseImagesOutput), 0o600))
	for _, arch := range []string{"amd64", "arm64"} {
		require.NoError(t, os.MkdirAll(f.artifact(arch), 0o700))
		f.writeArtifact(t, arch, "commit.sha", releaseImagesSHA+"\n")
		f.writeArtifact(t, arch, "version", releaseImagesVersion+"\n")
		f.writeArtifact(t, arch, "image.id", releaseImagesID(arch)+"\n")
		releaseImagesWriteJSON(t, filepath.Join(f.artifact(arch), "image.tar"), map[string]any{
			"Id": releaseImagesID(arch), "Architecture": arch, "Os": "linux",
			"Config": map[string]any{"Labels": map[string]any{
				"org.opencontainers.image.revision": releaseImagesSHA,
				"org.opencontainers.image.source":   "https://github.com/" + releaseImagesRepository,
				"org.opencontainers.image.version":  releaseImagesVersion,
			}},
		})
	}
	return f
}

func (f *releaseImagesFixture) path(parts ...string) string {
	return filepath.Join(append([]string{f.root}, parts...)...)
}

func (f *releaseImagesFixture) artifact(arch string) string {
	return f.path("image artifacts", "operator-"+releaseImagesSHA+"-"+arch)
}

func (f *releaseImagesFixture) writeArtifact(t *testing.T, arch, name, value string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.artifact(arch), name), []byte(value), 0o600))
}

func (f *releaseImagesFixture) mutateImage(t *testing.T, arch string, change func(map[string]any)) {
	t.Helper()
	path := filepath.Join(f.artifact(arch), "image.tar")
	image := releaseImagesReadJSON[map[string]any](t, path)
	change(image)
	releaseImagesWriteJSON(t, path, image)
}

func (f *releaseImagesFixture) run(t *testing.T, overrides map[string]string, args ...string) ([]byte, error) {
	t.Helper()
	releaseImagesWriteJSON(t, f.path("config.json"), f.config)
	// /bin/bash is Bash 3.2 on macOS: exercise the actual portable shell, not just
	// the workstation's potentially newer Homebrew Bash. The PATH always starts
	// with our Docker shim and never needs an installed Docker executable.
	cmd := exec.CommandContext(t.Context(), "/bin/bash", append([]string{f.path("release-images.sh")}, args...)...)
	cmd.Dir = f.root
	env := map[string]string{
		"PATH":                       f.path("bin") + string(os.PathListSeparator) + filepath.Dir(f.jq) + ":/usr/bin:/bin",
		"GORACE":                     "atexit_sleep_ms=0",
		"VERSION":                    releaseImagesVersion,
		"GITHUB_SHA":                 releaseImagesSHA,
		"GITHUB_REPOSITORY":          releaseImagesRepository,
		"IMAGE":                      releaseImagesImage,
		"IMAGE_TAGS":                 releaseImagesImage + ":" + releaseImagesVersion + "\n" + releaseImagesImage + ":v0.1\n" + releaseImagesImage + ":latest\n",
		"GITHUB_OUTPUT":              f.path("output"),
		"RELEASE_IMAGES_FAKE_DOCKER": "1",
		"RELEASE_IMAGES_TEST_ROOT":   f.root,
	}
	maps.Copy(env, overrides)
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	for _, call := range f.calls(t) {
		require.GreaterOrEqual(t, len(call), 2)
		switch call[0] {
		case "image":
			assert.Contains(t, []string{"load", "inspect", "tag", "push"}, call[1], "promotion must never rebuild")
		case "buildx":
			assert.Equal(t, "imagetools", call[1], "only index operations are allowed")
		default:
			t.Errorf("forbidden Docker operation (no builds/authentication): %v", call)
		}
	}
	if err != nil {
		return output, fmt.Errorf("running release image helper: %w", err)
	}
	return output, nil
}

func (f *releaseImagesFixture) calls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(f.path("calls.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var calls [][]string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var call []string
		require.NoError(t, json.Unmarshal([]byte(line), &call))
		calls = append(calls, call)
	}
	return calls
}

func (f *releaseImagesFixture) assertNoPublication(t *testing.T) {
	t.Helper()
	for _, call := range f.calls(t) {
		assert.Equal(t, "image", call[0])
		assert.Contains(t, []string{"load", "inspect"}, call[1], "failure must block all publication")
	}
}

func (f *releaseImagesFixture) assertOutput(t *testing.T, expected string) {
	t.Helper()
	data, err := os.ReadFile(f.path("output"))
	require.NoError(t, err)
	assert.Equal(t, expected, string(data))
}

func releaseImagesWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func releaseImagesReadJSON[T any](t *testing.T, path string) T {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var value T
	require.NoError(t, json.Unmarshal(data, &value))
	return value
}

func releaseImagesMap(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok, "expected an object in the test fixture")
	return object
}

func releaseImagesManifests(t *testing.T, index map[string]any) []any {
	t.Helper()
	manifests, ok := index["manifests"].([]any)
	require.True(t, ok, "expected manifest descriptors in the test fixture")
	return manifests
}

func releaseImagesDefaultIndex() map[string]any {
	// Reverse descriptor order and include the conventional arm64 variant to
	// prove validation does not depend on ordering or reject valid arm64/v8.
	manifests := make([]any, 0, 2)
	for _, arch := range []string{"arm64", "amd64"} {
		platform := map[string]any{"os": "linux", "architecture": arch}
		if arch == "arm64" {
			platform["variant"] = "v8"
		}
		manifests = append(manifests, map[string]any{
			"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": releaseImagesDigest(arch), "size": 1024,
			"platform": platform,
		})
	}
	return map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"digest": releaseImagesIndex, "size": 512, "manifests": manifests,
	}
}

func releaseImagesFakeDocker(root string, args []string) error {
	if root == "" || len(args) < 2 {
		return errors.New("missing isolated fake Docker configuration")
	}
	if err := releaseImagesRecordCall(root, args); err != nil {
		return err
	}
	docker, err := newReleaseImagesDocker(root)
	if err != nil {
		return err
	}
	prefix := docker.config.FailPrefix
	if len(prefix) > 0 && len(args) >= len(prefix) && slices.Equal(args[:len(prefix)], prefix) {
		return errors.New("injected Docker failure")
	}
	if err := docker.run(args); err != nil {
		return err
	}
	return docker.save(root)
}

func releaseImagesRecordCall(root string, args []string) error {
	log, err := os.OpenFile(filepath.Join(root, "calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening fake Docker call log: %w", err)
	}
	err = json.NewEncoder(log).Encode(args)
	closeErr := log.Close()
	if err != nil || closeErr != nil {
		return fmt.Errorf("recording fake Docker call: %w", errors.Join(err, closeErr))
	}
	return nil
}

type releaseImagesDocker struct {
	config releaseImagesFakeConfig
	state  releaseImagesFakeState
}

func newReleaseImagesDocker(root string) (*releaseImagesDocker, error) {
	configData, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("reading fake Docker configuration: %w", err)
	}
	docker := &releaseImagesDocker{state: releaseImagesFakeState{Images: map[string]map[string]any{}}}
	if err := json.Unmarshal(configData, &docker.config); err != nil {
		return nil, fmt.Errorf("decoding fake Docker configuration: %w", err)
	}
	stateData, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err == nil {
		if err := json.Unmarshal(stateData, &docker.state); err != nil {
			return nil, fmt.Errorf("decoding fake Docker state: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading fake Docker state: %w", err)
	}
	return docker, nil
}

func (d *releaseImagesDocker) save(root string) error {
	data, err := json.Marshal(d.state)
	if err != nil {
		return fmt.Errorf("encoding fake Docker state: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.json"), data, 0o600); err != nil {
		return fmt.Errorf("writing fake Docker state: %w", err)
	}
	return nil
}

func (d *releaseImagesDocker) run(args []string) error {
	switch args[0] {
	case "image":
		return d.runImage(args)
	case "buildx":
		return d.runIndex(args)
	default:
		return fmt.Errorf("forbidden or unsupported command: %v", args)
	}
}

func (d *releaseImagesDocker) runImage(args []string) error {
	switch {
	case len(args) == 4 && slices.Equal(args[:3], []string{"image", "load", "--input"}):
		return d.load(args[3])
	case len(args) == 3 && slices.Equal(args[:2], []string{"image", "inspect"}):
		return d.inspect(args[2])
	case len(args) == 4 && slices.Equal(args[:2], []string{"image", "tag"}):
		return d.tag(args[2], args[3])
	case len(args) == 3 && slices.Equal(args[:2], []string{"image", "push"}):
		return d.push(args[2])
	default:
		return fmt.Errorf("forbidden or unsupported image command: %v", args)
	}
}

func (d *releaseImagesDocker) runIndex(args []string) error {
	switch {
	case len(args) >= 3 && slices.Equal(args[:3], []string{"buildx", "imagetools", "create"}):
		return d.createIndex(args[3:])
	case len(args) == 6 && slices.Equal(args[:5], []string{"buildx", "imagetools", "inspect", "--format", "{{json .Manifest}}"}):
		return d.inspectIndex(args[5])
	default:
		return fmt.Errorf("forbidden or unsupported index command: %v", args)
	}
}

func (d *releaseImagesDocker) load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading fake image archive: %w", err)
	}
	var image map[string]any
	if err := json.Unmarshal(data, &image); err != nil {
		return fmt.Errorf("decoding fake image archive: %w", err)
	}
	arch := "amd64"
	if strings.HasSuffix(filepath.Dir(path), "-arm64") {
		arch = "arm64"
	}
	tag := "composite-ci:" + releaseImagesSHA + "-" + arch
	if override, found := d.config.LoadTags[arch]; found {
		tag = override
	}
	d.state.Images[tag] = image
	if id, ok := image["Id"].(string); ok {
		d.state.Images[id] = image
	}
	fmt.Println("Loaded image: " + tag)
	return nil
}

func (d *releaseImagesDocker) inspect(tag string) error {
	image, found := d.state.Images[tag]
	if !found {
		return errors.New("cannot inspect an image that was not loaded/tagged")
	}
	if d.config.InspectOutput != "" {
		fmt.Println(d.config.InspectOutput)
		return nil
	}
	if err := json.NewEncoder(os.Stdout).Encode([]map[string]any{image}); err != nil {
		return fmt.Errorf("encoding fake image inspection: %w", err)
	}
	return nil
}

func (d *releaseImagesDocker) tag(source, target string) error {
	image, found := d.state.Images[source]
	if !found || !strings.HasPrefix(source, "sha256:") {
		return errors.New("must tag the unchanged loaded image ID")
	}
	d.state.Images[target] = image
	return nil
}

func (d *releaseImagesDocker) push(tag string) error {
	image, found := d.state.Images[tag]
	if !found {
		return errors.New("cannot push an image that was not tagged")
	}
	arch := "amd64"
	if strings.HasSuffix(tag, "-arm64") {
		arch = "arm64"
	}
	digests := []string{"ghcr.io/another/repository@" + releaseImagesDigest(arch), releaseImagesImage + "@" + releaseImagesDigest(arch)}
	if override, found := d.config.RepoDigests[arch]; found {
		digests = override
	}
	image["RepoDigests"] = digests
	d.state.Pushed = append(d.state.Pushed, tag)
	fmt.Println(tag + ": digest: " + releaseImagesDigest(arch) + " size: 1024")
	return nil
}

func (d *releaseImagesDocker) createIndex(args []string) error {
	if len(d.state.Pushed) != 2 {
		return errors.New("index creation requires both platform pushes")
	}
	var sources []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--tag" && i+1 < len(args) {
			i++
			d.state.Aliases = append(d.state.Aliases, args[i])
		} else {
			sources = append(sources, args[i])
		}
	}
	if !slices.Equal(
		sources,
		[]string{releaseImagesImage + "@" + releaseImagesDigest("amd64"), releaseImagesImage + "@" + releaseImagesDigest("arm64")},
	) {
		return errors.New("index must use the two exact repository digest references")
	}
	return nil
}

func (d *releaseImagesDocker) inspectIndex(alias string) error {
	if !slices.Contains(d.state.Aliases, alias) {
		return errors.New("cannot inspect an alias that was not published")
	}
	manifest := d.config.Index
	if override, found := d.config.Indexes[alias]; found {
		manifest = override
	}
	if manifest == nil {
		var err error
		manifest, err = json.Marshal(releaseImagesDefaultIndex())
		if err != nil {
			return fmt.Errorf("encoding fake image index: %w", err)
		}
	}
	if d.config.IndexOutput != "" {
		fmt.Println(d.config.IndexOutput)
	} else {
		fmt.Println(string(manifest))
	}
	return nil
}
