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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	clusterctlv1 "sigs.k8s.io/cluster-api/cmd/clusterctl/api/v1alpha3"
	clusterctl "sigs.k8s.io/cluster-api/cmd/clusterctl/client"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client/cluster"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client/config"
	"sigs.k8s.io/yaml"
)

func TestClusterctlGenerateProvider(t *testing.T) {
	clusterctlIsolateConfig(t)
	componentsPath, image := clusterctlReleaseFixture(t)
	clusterctlAssertMetadata(t, filepath.Join(filepath.Dir(componentsPath), "metadata.yaml"))
	reader := config.NewMemoryReader()
	_, err := reader.AddProvider("composite", clusterctlv1.InfrastructureProviderType, componentsPath)
	require.NoError(t, err)
	require.NoError(t, reader.Init(t.Context(), ""))
	configuration, err := config.New(t.Context(), "", config.InjectReader(reader))
	require.NoError(t, err)
	for _, name := range []string{"COMPOSITE_WATCH_FILTER", "WATCH_FILTER"} {
		_, err := configuration.Variables().Get(name)
		require.Error(t, err, "%s must be genuinely absent, not configured as an empty variable", name)
	}
	clusterCalls := 0
	client, err := clusterctl.New(t.Context(), "", clusterctl.InjectConfig(configuration),
		clusterctl.InjectClusterClientFactory(func(clusterctl.ClusterClientFactoryInput) (cluster.Client, error) {
			clusterCalls++
			return nil, errors.New("GenerateProvider must never create a cluster client")
		}))
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		target    string
		namespace string
	}{
		{name: "default-namespace", namespace: "composite-system"},
		{name: "custom-namespace", target: "composite-custom-system", namespace: "composite-custom-system"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			components, err := client.GenerateProvider(t.Context(), "composite:v0.1.0", clusterctlv1.InfrastructureProviderType,
				clusterctl.ComponentsOptions{TargetNamespace: tc.target})
			require.NoError(t, err, "release assets must generate with both watch-filter variables absent")
			assert.Equal(t, "composite", components.Name())
			assert.Equal(t, clusterctlv1.InfrastructureProviderType, components.Type())
			assert.Equal(t, "v0.1.0", components.Version())
			assert.Equal(t, tc.namespace, components.TargetNamespace())
			assert.Empty(t, components.Variables(), "the release must not require installation variables")
			assert.Equal(t, []string{image}, components.Images())
			data, err := components.Yaml()
			require.NoError(t, err)
			assert.NotContains(t, string(data), "${", "generated YAML must contain no unresolved placeholders")
			clusterctlAssertObjects(t, components.Objs(), tc.namespace, image)
			inventory := components.InventoryObject()
			clusterctlAssertInventory(t, &inventory, tc.namespace)
		})
	}
	t.Run("unsupported-metadata-series", func(t *testing.T) {
		path := filepath.Join(filepath.Dir(componentsPath), "metadata.yaml")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(data), "minor: 1")
		require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "minor: 1", "minor: 2")), 0o600))
		components, err := client.GenerateProvider(t.Context(), "composite:v0.1.0", clusterctlv1.InfrastructureProviderType,
			clusterctl.ComponentsOptions{})
		require.ErrorContains(t, err, "does not match any release series")
		assert.Nil(t, components)
	})
	assert.Zero(t, clusterCalls, "generation and metadata validation must remain offline")
}

func TestClusterctlConfigurationPreservesDefaultRegistry(t *testing.T) {
	data, err := os.ReadFile("../clusterctl.yaml")
	require.NoError(t, err)
	var document struct {
		Providers []struct {
			Name string                    `json:"name"`
			Type clusterctlv1.ProviderType `json:"type"`
			URL  string                    `json:"url"`
		} `json:"providers"`
	}
	require.NoError(t, yaml.UnmarshalStrict(data, &document))
	require.Len(t, document.Providers, 1)
	provider := document.Providers[0]
	assert.Equal(t, "composite", provider.Name)
	assert.Equal(t, clusterctlv1.InfrastructureProviderType, provider.Type)
	assert.Equal(t, "https://github.com/numspot/cluster-api-provider-composite/releases/latest/infrastructure-components.yaml", provider.URL)

	reader := config.NewMemoryReader()
	require.NoError(t, reader.Init(t.Context(), ""))
	configuration, err := config.New(t.Context(), "", config.InjectReader(reader))
	require.NoError(t, err)
	defaults, err := configuration.Providers().List()
	require.NoError(t, err)
	_, err = configuration.Providers().Get("composite", clusterctlv1.InfrastructureProviderType)
	require.Error(t, err, "composite is custom configuration, not an upstream builtin")
	_, err = reader.AddProvider(provider.Name, provider.Type, provider.URL)
	require.NoError(t, err)
	providers, err := configuration.Providers().List()
	require.NoError(t, err)
	assert.Len(t, providers, len(defaults)+1)
	for _, original := range defaults {
		actual, err := configuration.Providers().Get(original.Name(), original.Type())
		require.NoError(t, err)
		assert.Equal(t, original.URL(), actual.URL(), "default provider %s/%s must remain unchanged", original.Type(), original.Name())
	}
	actual, err := configuration.Providers().Get("composite", clusterctlv1.InfrastructureProviderType)
	require.NoError(t, err)
	assert.Equal(t, provider.URL, actual.URL())
}

func clusterctlIsolateConfig(t *testing.T) {
	t.Helper()
	// Create the isolated override directory so upstream never takes its legacy
	// home fallback. Keep HOME unchanged so nested Go tools retain CI caches.
	t.Cleanup(xdg.Reload)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	xdg.Reload()
	directory, err := xdg.ConfigFile(config.ConfigFolderXDG)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(directory, "overrides"), 0o700))
	for _, name := range []string{"COMPOSITE_WATCH_FILTER", "WATCH_FILTER"} {
		previous, present := os.LookupEnv(name)
		require.NoError(t, os.Unsetenv(name))
		if present {
			t.Cleanup(func() { require.NoError(t, os.Setenv(name, previous)) })
		}
	}
}

func clusterctlReleaseFixture(t *testing.T) (componentsPath, image string) {
	t.Helper()
	root := helmPackagingFixture(t)
	require.NoError(t, os.CopyFS(filepath.Join(root, "config"), os.DirFS("../config")))
	for _, path := range []string{"hack/release-manifests.sh", "api/config/crd/kustomization.yaml"} {
		data, err := os.ReadFile(filepath.Join("..", path))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, path), data, 0o600))
	}
	image = "registry.example.org/another-owner/composite@sha256:" + strings.Repeat("a", 64)
	cmd := exec.CommandContext(t.Context(), "make", "release-manifests", "VERSION=v0.1.0", "IMAGE="+image)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GO="+helmPackagingTool(t, "GO", "go"))
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "generating real release assets in scratch: %s", output)
	return filepath.Join(root, "out", "repository", "infrastructure-composite", "v0.1.0", "infrastructure-components.yaml"), image
}

func clusterctlAssertMetadata(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var metadata clusterctlv1.Metadata
	require.NoError(t, yaml.UnmarshalStrict(data, &metadata))
	assert.Equal(t, clusterctlv1.GroupVersion.String(), metadata.APIVersion)
	assert.Equal(t, "Metadata", metadata.Kind)
	assert.Equal(t, []clusterctlv1.ReleaseSeries{{Major: 0, Minor: 1, Contract: "v1beta2"}}, metadata.ReleaseSeries)
}

func clusterctlAssertObjects(t *testing.T, objects []unstructured.Unstructured, namespace, image string) {
	t.Helper()
	byKind := map[string][]unstructured.Unstructured{}
	for _, object := range objects {
		assert.Equal(t, "infrastructure-composite", object.GetLabels()[clusterv1.ProviderNameLabel], object.GetName())
		assert.Contains(t, object.GetLabels(), clusterctlv1.ClusterctlLabel, object.GetName())
		byKind[object.GetKind()] = append(byKind[object.GetKind()], object)
	}
	require.Len(t, byKind["Namespace"], 1)
	assert.Equal(t, namespace, byKind["Namespace"][0].GetName())
	clusterctlAssertCRDs(t, byKind["CustomResourceDefinition"])
	require.Len(t, byKind["Deployment"], 1)
	deployment := &appsv1.Deployment{}
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(byKind["Deployment"][0].Object, deployment))
	assert.Equal(t, namespace, deployment.Namespace)
	assert.Equal(t, "composite-controller-manager", deployment.Name)
	assert.Equal(t, "composite-manager", deployment.Spec.Template.Spec.ServiceAccountName)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, image, deployment.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, []corev1.EnvVar{{Name: "WATCH_FILTER", Value: ""}}, deployment.Spec.Template.Spec.Containers[0].Env)
	require.Len(t, byKind["ServiceAccount"], 1)
	assert.Equal(t, namespace, byKind["ServiceAccount"][0].GetNamespace())
	assert.Equal(t, "composite-manager", byKind["ServiceAccount"][0].GetName())
	require.Len(t, byKind["ClusterRoleBinding"], 1)
	binding := &rbacv1.ClusterRoleBinding{}
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(byKind["ClusterRoleBinding"][0].Object, binding))
	assert.Empty(t, binding.Namespace)
	assert.Equal(t, rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "composite-manager-role"}, binding.RoleRef)
	assert.Equal(t, []rbacv1.Subject{{Kind: "ServiceAccount", Name: "composite-manager", Namespace: namespace}}, binding.Subjects)
	roles := make([]string, 0, len(byKind["ClusterRole"]))
	for _, role := range byKind["ClusterRole"] {
		assert.Empty(t, role.GetNamespace())
		roles = append(roles, role.GetName())
	}
	assert.Contains(t, roles, binding.RoleRef.Name)
}

func clusterctlAssertCRDs(t *testing.T, objects []unstructured.Unstructured) {
	t.Helper()
	require.Len(t, objects, 4)
	want := map[string]apiextensionsv1.ResourceScope{
		"compositeclusters":          apiextensionsv1.NamespaceScoped,
		"compositeclustertemplates":  apiextensionsv1.NamespaceScoped,
		"compositeproviders":         apiextensionsv1.NamespaceScoped,
		"compositeprovidertemplates": apiextensionsv1.ClusterScoped,
	}
	for _, object := range objects {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, crd))
		scope, found := want[crd.Spec.Names.Plural]
		require.True(t, found, "unexpected CRD %s", crd.Name)
		delete(want, crd.Spec.Names.Plural)
		assert.Empty(t, crd.Namespace)
		assert.Equal(t, "infrastructure.cluster.x-k8s.io", crd.Spec.Group)
		assert.Equal(t, scope, crd.Spec.Scope)
		require.Len(t, crd.Spec.Versions, 1)
		assert.Equal(t, "v1beta2", crd.Spec.Versions[0].Name)
		assert.True(t, crd.Spec.Versions[0].Served)
		assert.True(t, crd.Spec.Versions[0].Storage)
		contract := crd.Labels["cluster.x-k8s.io/v1beta2"]
		if crd.Spec.Names.Kind == "CompositeCluster" || crd.Spec.Names.Kind == "CompositeClusterTemplate" {
			assert.Equal(t, "v1beta2", contract)
		} else {
			assert.Empty(t, contract, "attachment/profile CRDs must not advertise the InfraCluster contract")
		}
		assert.NotContains(t, crd.Labels, "cluster.x-k8s.io/v1beta1")
	}
	assert.Empty(t, want)
}

func clusterctlAssertInventory(t *testing.T, provider *clusterctlv1.Provider, namespace string) {
	t.Helper()
	assert.Equal(t, clusterctlv1.GroupVersion.String(), provider.APIVersion)
	assert.Equal(t, "Provider", provider.Kind)
	assert.Equal(t, "composite", provider.ProviderName)
	assert.Equal(t, "infrastructure-composite", provider.Name)
	assert.Equal(t, namespace, provider.Namespace)
	assert.Equal(t, string(clusterctlv1.InfrastructureProviderType), provider.Type)
	assert.Equal(t, "v0.1.0", provider.Version)
	assert.Equal(t, map[string]string{
		clusterv1.ProviderNameLabel:      "infrastructure-composite",
		clusterctlv1.ClusterctlLabel:     "",
		clusterctlv1.ClusterctlCoreLabel: clusterctlv1.ClusterctlCoreLabelInventoryValue,
	}, provider.Labels)
}
