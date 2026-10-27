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

package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestStageChartSetsOnlyImageDefaults(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	for _, tc := range []struct {
		name       string
		image      string
		repository string
		tag        string
		digest     string
	}{
		{
			name:       "tag-clears-old-digest",
			image:      "example.org:5443/another-owner/controller:custom-tag",
			repository: "example.org:5443/another-owner/controller",
			tag:        "custom-tag",
		},
		{
			name:       "digest-clears-old-tag",
			image:      "example.org/another-owner/controller@" + digest,
			repository: "example.org/another-owner/controller",
			digest:     digest,
		},
		{
			name:       "digest-takes-precedence-over-tag",
			image:      "example.org/another-owner/controller:custom-tag@" + digest,
			repository: "example.org/another-owner/controller",
			digest:     digest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := stageChartFixture(t)
			before := stageChartFiles(t, source)
			target := filepath.Join(t.TempDir(), "staged")
			require.NoError(t, stageChart(source, target, tc.image))
			assert.Equal(t, before, stageChartFiles(t, source), "staging must never modify source files")
			after := stageChartFiles(t, target)
			require.Len(t, after, len(before))
			for path, data := range before {
				if path != "values.yaml" {
					assert.Equal(t, data, after[path], "copied payload %s", path)
				}
			}
			var values struct {
				Image struct {
					Repository string `json:"repository"`
					Tag        string `json:"tag"`
					Digest     string `json:"digest"`
					PullPolicy string `json:"pullPolicy"`
					Custom     bool   `json:"custom"`
				} `json:"image"`
				Payload json.RawMessage `json:"payload"`
			}
			require.NoError(t, yaml.Unmarshal(after["values.yaml"], &values))
			assert.Equal(t, tc.repository, values.Image.Repository)
			assert.Equal(t, tc.tag, values.Image.Tag)
			assert.Equal(t, tc.digest, values.Image.Digest)
			assert.Equal(t, "Always", values.Image.PullPolicy)
			assert.True(t, values.Image.Custom)
			assert.JSONEq(t, `{"integer":9007199254740993,"text":"001","list":[true,null,"unchanged"]}`, string(values.Payload))
			assert.Contains(t, string(values.Payload), "9007199254740993", "staging must not round exact integer payloads")
		})
	}
}

func TestStageChartRejectsMalformedImagesBeforeCopy(t *testing.T) {
	for _, image := range []string{"", "example.org/controller", "example.org/INVALID:dev", "example.org/controller@sha256:bad"} {
		t.Run(image, func(t *testing.T) {
			source := stageChartFixture(t)
			before := stageChartFiles(t, source)
			target := filepath.Join(t.TempDir(), "staged")
			require.Error(t, stageChart(source, target, image))
			_, err := os.Stat(target)
			assert.ErrorIs(t, err, fs.ErrNotExist)
			assert.Equal(t, before, stageChartFiles(t, source))
		})
	}
}

func TestStageChartRejectsInvalidValues(t *testing.T) {
	for _, values := range []string{
		"image: [invalid\n",
		"image: null\n",
		"image: [not, an, object]\n",
		"replicaCount: 1\n",
		"image: {}\nimage: {}\n",
	} {
		t.Run(values, func(t *testing.T) {
			source := stageChartFixture(t)
			require.NoError(t, os.WriteFile(filepath.Join(source, "values.yaml"), []byte(values), 0o600))
			before := stageChartFiles(t, source)
			require.Error(t, stageChart(source, filepath.Join(t.TempDir(), "staged"), "example.org/controller:dev"))
			assert.Equal(t, before, stageChartFiles(t, source))
		})
	}
}

func TestStageChartRejectsLinkedValues(t *testing.T) {
	original := stageChartFixture(t)
	before := stageChartFiles(t, original)
	source := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(original, "values.yaml"), filepath.Join(source, "values.yaml")))
	require.Error(t, stageChart(source, filepath.Join(t.TempDir(), "staged"), "example.org/controller:dev"))
	assert.Equal(t, before, stageChartFiles(t, original), "staging must not write through values symlinks")
}

func TestStageChartDoesNotOverwriteExistingDestination(t *testing.T) {
	source := stageChartFixture(t)
	before := stageChartFiles(t, source)
	require.Error(t, stageChart(source, source, "example.org/controller:dev"))
	assert.Equal(t, before, stageChartFiles(t, source))
}

func stageChartFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: composite\nversion: 0.1.0-alpha.1\nappVersion: v0.1.0-alpha.1\n",
		"values.yaml": "image:\n  repository: original.example.org/controller\n  tag: old-tag\n  digest: sha256:" + strings.Repeat("a", 64) + "\n" +
			"  pullPolicy: Always\n  custom: true\npayload:\n  integer: 9007199254740993\n  text: \"001\"\n  list: [true, null, unchanged]\n",
		"values.schema.json":      `{"type":"object"}`,
		".helmignore":             "*.tmp\n",
		"templates/_helpers.tpl":  "{{- define \"composite.example\" -}}unchanged{{- end -}}\n",
		"crds/example.yaml":       "# preserve the entire CRD payload\nnumber: 9007199254740993\n",
		"nested/additional.yaml":  "preserved: true\n",
		"nested/additional.bin":   "\x00\x01\x02",
		"nested/README.md":        "# Additional payload\n",
		"templates/workload.yaml": "image: {{ .Values.image.repository }}\n",
	}
	for path, data := range files {
		target := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, []byte(data), 0o600))
	}
	return root
}

func stageChartFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		require.NoError(t, err)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		files[filepath.ToSlash(relative)] = data
		return nil
	})
	require.NoError(t, err)
	return files
}
