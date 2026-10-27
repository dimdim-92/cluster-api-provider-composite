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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestHelmPackageUsesImageDefaults(t *testing.T) {
	helm := helmPackagingTool(t, "HELM", "helm")
	goTool := helmPackagingTool(t, "GO", "go")
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name       string
		image      string
		repository string
		tag        string
		digest     string
	}{
		{
			name:       "alternate-owner-tag",
			image:      "registry.example.org:5443/another-owner/composite:custom-build",
			repository: "registry.example.org:5443/another-owner/composite",
			tag:        "custom-build",
		},
		{
			name:       "alternate-owner-digest",
			image:      "registry.example.org/another-owner/composite@" + digest,
			repository: "registry.example.org/another-owner/composite",
			digest:     digest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := helmPackagingFixture(t)
			before := helmPackagingSources(t, root)
			version := "v0.1.7-alpha.9"
			cmd := exec.CommandContext(t.Context(), "make", "helm-package", "VERSION="+version, "IMAGE="+tc.image)
			cmd.Dir = root
			cmd.Env = helmPackagingEnvironment(goTool, helm)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "packaging Helm chart: %s", output)
			assert.Equal(t, before, helmPackagingSources(t, root), "packaging must not modify the source chart or CRDs")

			archive := filepath.Join(root, "out", "cluster-api-provider-composite-0.1.7-alpha.9.tgz")
			files := helmPackagingArchive(t, archive)
			prefix := "cluster-api-provider-composite/"
			var values struct {
				Image struct {
					Repository string `json:"repository"`
					Tag        string `json:"tag"`
					Digest     string `json:"digest"`
				} `json:"image"`
			}
			require.NoError(t, yaml.Unmarshal(files[prefix+"values.yaml"], &values))
			assert.Equal(t, tc.repository, values.Image.Repository)
			assert.Equal(t, tc.tag, values.Image.Tag)
			assert.Equal(t, tc.digest, values.Image.Digest)
			var metadata map[string]any
			require.NoError(t, yaml.Unmarshal(files[prefix+"Chart.yaml"], &metadata))
			assert.Equal(t, "0.1.7-alpha.9", metadata["version"])
			assert.Equal(t, version, metadata["appVersion"])
			for path, file := range before {
				if !strings.HasPrefix(path, "helm/") {
					continue
				}
				relative := strings.TrimPrefix(path, "helm/")
				if relative == "Chart.yaml" || relative == "values.yaml" || relative == ".helmignore" {
					continue
				}
				assert.Equal(t, file.data, files[prefix+relative], "packaged payload %s", relative)
			}

			cmd = exec.CommandContext(t.Context(), helm, "template", "composite", archive, "--namespace=composite-system", "--include-crds")
			output, err = cmd.CombinedOutput()
			require.NoError(t, err, "rendering packaged chart: %s", output)
			pod := installerPodSpec(t, output)
			require.Len(t, pod.Containers, 1)
			assert.Equal(t, tc.image, pod.Containers[0].Image)
		})
	}
}

func TestHelmPackageRejectsMalformedInputs(t *testing.T) {
	goTool := helmPackagingTool(t, "GO", "go")
	for _, tc := range []struct {
		name    string
		version string
		image   string
	}{
		{name: "version-without-v", version: "0.1.0", image: "example.org/composite:dev"},
		{name: "malformed-version", version: "v0.1.0-alpha..1", image: "example.org/composite:dev"},
		{name: "unsupported-release-series", version: "v0.2.0", image: "example.org/composite:dev"},
		{name: "untagged-image", version: "v0.1.0", image: "example.org/composite"},
		{name: "invalid-repository", version: "v0.1.0", image: "example.org/INVALID:dev"},
		{name: "invalid-digest", version: "v0.1.0", image: "example.org/composite@sha256:bad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := helmPackagingFixture(t)
			before := helmPackagingSources(t, root)
			helm := filepath.Join(t.TempDir(), "helm")
			marker := filepath.Join(t.TempDir(), "helm-called")
			require.NoError(t, os.WriteFile(helm, []byte("#!/bin/sh\n: > \"$HELM_MARKER\"\nexit 0\n"), 0o700))
			cmd := exec.CommandContext(t.Context(), "make", "helm-package", "VERSION="+tc.version, "IMAGE="+tc.image)
			cmd.Dir = root
			cmd.Env = append(helmPackagingEnvironment(goTool, helm), "HELM_MARKER="+marker)
			output, err := cmd.CombinedOutput()
			require.Error(t, err, "invalid release inputs must fail: %s", output)
			_, err = os.Stat(marker)
			assert.ErrorIs(t, err, fs.ErrNotExist, "Helm must not run for invalid release inputs")
			_, err = os.Stat(filepath.Join(root, "out"))
			assert.ErrorIs(t, err, fs.ErrNotExist, "invalid release inputs must not create output")
			assert.Equal(t, before, helmPackagingSources(t, root))
		})
	}
}

func TestHelmPackageUsesSelectedGoAndCleansFailedStage(t *testing.T) {
	goTool := helmPackagingTool(t, "GO", "go")
	root := helmPackagingFixture(t)
	before := helmPackagingSources(t, root)
	tools := t.TempDir()
	selected := filepath.Join(tools, "selected go")
	helm := filepath.Join(tools, "helm")
	goMarker := filepath.Join(tools, "selected-go-called")
	helmMarker := filepath.Join(tools, "staged-chart")
	require.NoError(t, os.WriteFile(selected, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GO_MARKER\"\nexec \"$REAL_GO\" \"$@\"\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(tools, "go"), []byte("#!/bin/sh\nexit 91\n"), 0o700))
	require.NoError(t, os.WriteFile(helm, []byte("#!/bin/sh\nprintf '%s\\n' \"$2\" > \"$HELM_MARKER\"\nexit 29\n"), 0o700))
	cmd := exec.CommandContext(t.Context(), "make", "helm-package", "VERSION=v0.1.0-alpha.1", "IMAGE=example.org/another-owner/composite:custom-tag")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GO="+selected, "HELM="+helm, "REAL_GO="+goTool, "GO_MARKER="+goMarker, "HELM_MARKER="+helmMarker,
		"PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "Helm failure must propagate: %s", output)
	data, err := os.ReadFile(goMarker)
	require.NoError(t, err)
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, calls, 2, "selected Go must run both the release verifier and staging helper")
	assert.Equal(t, "run ./hack/check-release", calls[0])
	assert.True(t, strings.HasPrefix(calls[1], "run ./hack/package-helm helm "), calls[1])
	data, err = os.ReadFile(helmMarker)
	require.NoError(t, err, "Helm must receive the staged chart")
	stage := strings.TrimSpace(string(data))
	assert.NotEqual(t, filepath.Join(root, "helm"), stage)
	_, err = os.Stat(filepath.Dir(stage))
	assert.ErrorIs(t, err, fs.ErrNotExist, "failed packaging must clean its scratch directory")
	assert.Equal(t, before, helmPackagingSources(t, root))
}

func TestVerifyVersionHonorsGOOverride(t *testing.T) {
	root := helmPackagingFixture(t)
	tools := t.TempDir()
	selected := filepath.Join(tools, "selected go")
	marker := filepath.Join(tools, "selected-go-called")
	require.NoError(t, os.WriteFile(selected, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$GO_MARKER\"\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(tools, "go"), []byte("#!/bin/sh\nexit 91\n"), 0o700))
	cmd := exec.CommandContext(t.Context(), "sh", "hack/verify-version.sh")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GO="+selected, "GO_MARKER="+marker, "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "verifier must invoke selected GO instead of PATH go: %s", output)
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	assert.Equal(t, "run ./hack/check-release\n", string(data))
}

func helmPackagingTool(t *testing.T, variable, fallback string) string {
	t.Helper()
	tool := os.Getenv(variable)
	if tool == "" {
		tool = fallback
	}
	path, err := exec.LookPath(tool)
	if err != nil {
		t.Skipf("%s is unavailable: %v", variable, err)
	}
	absolute, err := filepath.Abs(path)
	require.NoError(t, err)
	return absolute
}

func helmPackagingEnvironment(goTool, helm string) []string {
	return append(os.Environ(), "GO="+goTool, "HELM="+helm,
		"PATH="+filepath.Dir(goTool)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func helmPackagingFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	paths := []string{
		"Makefile", "go.mod", "go.sum", "metadata.yaml", "hack/tools.env", "hack/verify-version.sh", "hack/check-release",
		"hack/helm-package.sh", "hack/package-helm", "helm", "api/config/crd/bases",
	}
	for _, path := range paths {
		source := filepath.Join("..", path)
		info, err := os.Stat(source)
		require.NoError(t, err)
		target := filepath.Join(root, path)
		if info.IsDir() {
			require.NoError(t, os.CopyFS(target, os.DirFS(source)))
			continue
		}
		data, err := os.ReadFile(source)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, data, info.Mode().Perm()))
	}
	return root
}

type helmPackagingSource struct {
	data []byte
	mode fs.FileMode
}

func helmPackagingSources(t *testing.T, root string) map[string]helmPackagingSource {
	t.Helper()
	files := map[string]helmPackagingSource{}
	for _, directory := range []string{"helm", "api/config/crd/bases"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, walkErr error) error {
			require.NoError(t, walkErr)
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			require.NoError(t, err)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			info, err := entry.Info()
			require.NoError(t, err)
			files[filepath.ToSlash(relative)] = helmPackagingSource{data: data, mode: info.Mode()}
			return nil
		})
		require.NoError(t, err)
	}
	return files
}

func helmPackagingArchive(t *testing.T, path string) map[string][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	defer func() { require.NoError(t, compressed.Close()) }()
	archive := tar.NewReader(compressed)
	files := map[string][]byte{}
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		require.NoError(t, err)
		data, err := io.ReadAll(archive)
		require.NoError(t, err)
		files[header.Name] = data
	}
}
