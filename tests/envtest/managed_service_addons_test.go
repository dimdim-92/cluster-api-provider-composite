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
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/profile"
)

func TestManagedServiceProviderAddonTemplates(t *testing.T) {
	sources := readManagedServiceSourceTemplates(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "local", "managed-service", "provider-addons", "templates.yaml"))
	require.NoError(t, err)
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	count := 0
	for {
		p := &infrav1.CompositeProviderTemplate{}
		err := decoder.Decode(p)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.Equal(t, "CompositeProviderTemplate", p.Kind)
		require.NoError(t, k8s.Create(t.Context(), p))
		configuration := &profile.Configuration{
			Defaults: &profile.Defaults{
				InfraSpec: managedServiceSourcePayload(t, sources, p.Spec.InfrastructureTemplateRef),
				Bootstrap: managedServiceSourcePayload(t, sources, p.Spec.BootstrapTemplateRef),
			},
			OverridePolicy: p.Spec.OverridePolicy,
			EndpointPaths:  p.Spec.EndpointPaths,
			CredentialsRef: infrav1.ProviderCredentialsReference{Name: "namespace-local-credentials", NamePaths: p.Spec.CredentialsNamePaths},
		}
		_, err = profile.Resolve(configuration, nil, nil, nil)
		require.NoError(t, err)
		count++
	}
	assert.Equal(t, 3, count)
}

func TestManagedServiceProviderAddonGraph(t *testing.T) {
	for _, backend := range []string{"outscale", "openstack"} {
		t.Run(backend, func(t *testing.T) {
			var class unstructured.Unstructured
			readManagedServiceExample(t, "provider-addons/"+backend+"-class.yaml", &class)
			provider, found, err := unstructured.NestedMap(class.Object, "spec", "resource", "provider", "manifest")
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, "CompositeProvider", provider["kind"])
			for _, field := range []string{"clusterRef", "templateRef", "credentialsRef"} {
				_, found, err := unstructured.NestedMap(provider, "spec", field)
				require.NoError(t, err)
				require.True(t, found)
			}
			addons, found, err := unstructured.NestedSlice(class.Object, "spec", "addonDefinitions")
			require.NoError(t, err)
			require.True(t, found)
			require.NotEmpty(t, addons)
			for _, item := range addons {
				addon, ok := item.(map[string]any)
				require.True(t, ok)
				name, _, err := unstructured.NestedString(addon, "classRef", "name")
				require.NoError(t, err)
				assert.Equal(t, "composite-"+backend+"-nodepool-v1", name)
				patches, found, err := unstructured.NestedSlice(addon, "patches")
				require.NoError(t, err)
				require.True(t, found)
				assert.GreaterOrEqual(t, len(patches), 2)
			}
		})
	}
}

func TestManagedServiceProviderAddonReferenceOutput(t *testing.T) {
	var p infrav1.CompositeProvider
	readManagedServiceExample(t, "provider-addons/provider.yaml", &p)
	p.Namespace = newNamespace(t)
	require.NoError(t, k8s.Create(t.Context(), &p))
	fresh := &infrav1.CompositeProvider{}
	require.NoError(t, k8s.Get(t.Context(), client.ObjectKeyFromObject(&p), fresh))
	assert.Equal(t, p.Spec, fresh.Spec)
}
