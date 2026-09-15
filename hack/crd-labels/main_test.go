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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestApplyLabels_RemovesUnsupportedContract(t *testing.T) {
	metadata := map[string]any{"labels": map[string]any{
		"cluster.x-k8s.io/v1beta1": "v1beta2", "example.org/retained": "true",
	}}
	labels, changed := applyLabels(metadata, true)
	assert.True(t, changed)
	assert.NotContains(t, labels, "cluster.x-k8s.io/v1beta1")
	assert.Equal(t, "v1beta2", labels["cluster.x-k8s.io/v1beta2"])
	assert.Equal(t, "true", labels["example.org/retained"])
	metadata["labels"] = labels
	_, changed = applyLabels(metadata, true)
	assert.False(t, changed)
}

func TestStamp_ProfileDoesNotAdvertiseInfrastructureContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.yaml")
	input := []byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: compositeproviders.infrastructure.cluster.x-k8s.io
  labels:
    cluster.x-k8s.io/v1: v1beta2
    cluster.x-k8s.io/v1beta1: v1beta2
    cluster.x-k8s.io/v1beta2: v1beta2
    example.org/retained: "true"
spec:
  group: infrastructure.cluster.x-k8s.io
  names:
    kind: CompositeProvider
`)
	require.NoError(t, os.WriteFile(path, input, 0o600))
	_, err := reconcileLabels(path, false)
	require.ErrorIs(t, err, errIncorrectLabels)
	unchanged, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, input, unchanged, "label verification must not repair source")
	changed, err := reconcileLabels(path, true)
	require.NoError(t, err)
	assert.True(t, changed)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc struct {
		Metadata struct{ Labels map[string]string }
	}
	require.NoError(t, yaml.Unmarshal(data, &doc))
	assert.Equal(t, "infrastructure-composite", doc.Metadata.Labels["cluster.x-k8s.io/provider"])
	assert.NotContains(t, doc.Metadata.Labels, "cluster.x-k8s.io/v1beta1")
	assert.NotContains(t, doc.Metadata.Labels, "cluster.x-k8s.io/v1")
	assert.NotContains(t, doc.Metadata.Labels, "cluster.x-k8s.io/v1beta2")
	assert.Equal(t, "true", doc.Metadata.Labels["example.org/retained"])
	changed, err = reconcileLabels(path, true)
	require.NoError(t, err)
	assert.False(t, changed)
	_, err = reconcileLabels(path, false)
	require.NoError(t, err)
}

func TestResourceContractClassification(t *testing.T) {
	for _, kind := range []string{"CompositeCluster", "CompositeClusterTemplate", "CompositeProvider", "Unexpected"} {
		t.Run(kind, func(t *testing.T) {
			doc := map[string]any{"spec": map[string]any{"group": "infrastructure.cluster.x-k8s.io", "names": map[string]any{"kind": kind}}}
			contract, err := implementsInfraContract(doc)
			if kind == "Unexpected" {
				require.ErrorIs(t, err, errUnknownResource)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, kind != "CompositeProvider", contract)
		})
	}
}
