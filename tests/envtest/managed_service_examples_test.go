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

package envtest_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func TestManagedService(t *testing.T) {
	var class unstructured.Unstructured
	readManagedServiceExample(t, "service-cluster/cluster-class.yaml", &class)
	variables, found, err := unstructured.NestedSlice(class.Object, "spec", "variables")
	require.NoError(t, err)
	require.True(t, found)
	for _, item := range variables {
		variable, ok := item.(map[string]any)
		require.True(t, ok)
		assert.NotEqual(t, "providers", variable["name"])
	}
	patches, found, err := unstructured.NestedSlice(class.Object, "spec", "patches")
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, patches)
	for _, item := range patches {
		patch, ok := item.(map[string]any)
		require.True(t, ok)
		definitions, ok := patch["definitions"].([]any)
		require.True(t, ok)
		for _, item := range definitions {
			definition, ok := item.(map[string]any)
			require.True(t, ok)
			selector, ok := definition["selector"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "KamajiControlPlaneTemplate", selector["kind"], "topology only configures the hosted control plane")
		}
	}
	for _, path := range []string{"composite-cluster.yaml", "service-cluster/composite-cluster-template.yaml"} {
		var obj unstructured.Unstructured
		readManagedServiceExample(t, path, &obj)
		fields := []string{"spec"}
		if obj.GetKind() == "CompositeClusterTemplate" {
			fields = []string{"spec", "template", "spec"}
		}
		spec, found, err := unstructured.NestedMap(obj.Object, fields...)
		require.NoError(t, err)
		require.True(t, found)
		assert.Empty(t, spec)
	}
	for _, path := range []string{"cluster.yaml", "instance.yaml", "instance-empty.yaml", "management-cluster/cluster-class.yaml"} {
		var obj unstructured.Unstructured
		readManagedServiceExample(t, path, &obj)
		encoded, err := json.Marshal(obj.Object)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), `"providers"`, "provider configuration is not a topology/MSC variable")
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "local", "managed-service", "providers.yaml"))
	require.NoError(t, err)
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	names := map[string]bool{}
	for {
		p := &infrav1.CompositeProvider{}
		err := decoder.Decode(p)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.Equal(t, "CompositeProvider", p.Kind)
		assert.Equal(t, "cluster-demo-infra", p.Spec.ClusterRef.Name)
		assert.NotEmpty(t, p.Spec.TemplateRef.Name)
		assert.NotEmpty(t, p.Spec.InfraClusterName)
		assert.False(t, names[p.Name], "attachment names must be unique")
		names[p.Name] = true
		p.Namespace = newNamespace(t)
		require.NoError(t, k8s.Create(t.Context(), p), "standalone provider fixtures must pass admission")
	}
	assert.Len(t, names, 3)
}

func readManagedServiceSourceTemplates(t *testing.T) map[string]*unstructured.Unstructured {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "local", "managed-service", "service-cluster", "provider-templates.yaml"))
	require.NoError(t, err)
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	sources := map[string]*unstructured.Unstructured{}
	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(obj)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		key := obj.GetAPIVersion() + "/" + obj.GetKind() + "/" + obj.GetNamespace() + "/" + obj.GetName()
		sources[key] = obj
	}
	return sources
}

func managedServiceSourcePayload(
	t *testing.T,
	sources map[string]*unstructured.Unstructured,
	ref infrav1.ProviderTemplateReference,
) *apiextensionsv1.JSON {
	t.Helper()
	obj := sources[ref.APIVersion+"/"+ref.Kind+"/"+ref.Namespace+"/"+ref.Name]
	require.NotNil(t, obj, "source template must exist in the example bundle")
	payload, found, err := unstructured.NestedMap(obj.Object, "spec", "template", "spec")
	require.NoError(t, err)
	require.True(t, found)
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	return &apiextensionsv1.JSON{Raw: encoded}
}

func readManagedServiceExample(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "local", "managed-service", path))
	require.NoError(t, err)
	require.NoError(t, yaml.UnmarshalStrict(data, target))
}
