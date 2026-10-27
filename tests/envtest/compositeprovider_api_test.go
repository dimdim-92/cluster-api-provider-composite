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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
)

func TestCompositeProviderAPI_ScopeAndContract(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, k8s.Get(t.Context(), client.ObjectKey{Name: "compositeprovidertemplates.infrastructure.cluster.x-k8s.io"}, crd))
	assert.Equal(t, apiextensionsv1.ClusterScoped, crd.Spec.Scope)
	assert.Equal(t, "infrastructure-composite", crd.Labels["cluster.x-k8s.io/provider"])
	assert.NotContains(t, crd.Labels, "cluster.x-k8s.io/v1beta1")
	assert.NotContains(t, crd.Labels, "cluster.x-k8s.io/v1beta2")
	_, err := capi.NewContractCache(k8s).Get(t.Context(), infrav1.GroupVersion.WithKind("CompositeProviderTemplate").GroupKind())
	require.ErrorIs(t, err, capi.ErrInvalidContract, "a cluster-scoped configuration profile is not a namespaced InfraCluster")
	obj := compositeProviderObject()
	require.NoError(t, k8s.Create(t.Context(), obj))
	assert.Empty(t, obj.GetNamespace())
	before := obj.DeepCopy()
	obj.SetLabels(map[string]string{"test.example.org/metadata": "editable"})
	require.NoError(t, k8s.Update(t.Context(), obj))
	obj.Object["status"] = map[string]any{"observedGeneration": int64(1)}
	require.NoError(t, k8s.Status().Update(t.Context(), obj))
	assert.Equal(t, before.Object["spec"], obj.Object["spec"], "metadata/status updates must retain opaque configuration")
	require.NoError(t, k8s.Update(t.Context(), obj), "identical spec resubmission remains valid")
}

func TestCompositeProviderAPI_ImmutableSpecification(t *testing.T) {
	for _, operation := range []string{"update", "merge", "apply"} {
		for _, change := range []string{"provider", "region", "account", "native-kind", "zones", "endpoint", "infrastructure-template", "bootstrap-template", "template-namespace", "policy"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				obj := compositeProviderObject()
				if operation == "apply" {
					// An SSA omission deletes only fields owned by that manager.
					obj.SetName("profile-" + newNamespace(t))
					obj.SetGenerateName("")
					require.NoError(
						t,
						k8s.Apply(t.Context(), client.ApplyConfigurationFromUnstructured(obj), client.FieldOwner("profile-immutability-test")),
					)
				} else {
					require.NoError(t, k8s.Create(t.Context(), obj))
				}
				before := obj.DeepCopy()
				changeProfileSpec(t, obj, change)
				var err error
				switch operation {
				case "update":
					err = k8s.Update(t.Context(), obj)
				case "merge":
					err = k8s.Patch(t.Context(), obj, client.MergeFrom(before))
				case "apply":
					obj.SetManagedFields(nil)
					err = k8s.Apply(
						t.Context(),
						client.ApplyConfigurationFromUnstructured(obj),
						client.FieldOwner("profile-immutability-test"),
						client.ForceOwnership,
					)
				}
				require.True(t, apierrors.IsInvalid(err), "profile mutation must fail admission: %v", err)
				assert.ErrorContains(t, err, "CompositeProviderTemplate spec is immutable")
				require.NoError(t, k8s.Get(t.Context(), client.ObjectKeyFromObject(before), obj))
				assert.Equal(t, before.Object["spec"], obj.Object["spec"])
				assert.Equal(t, before.GetUID(), obj.GetUID())
				assert.Equal(t, before.GetResourceVersion(), obj.GetResourceVersion())
			})
		}
	}
}

func changeProfileSpec(t *testing.T, obj *unstructured.Unstructured, change string) {
	t.Helper()
	if change == "native-kind" {
		require.NoError(t, unstructured.SetNestedField(obj.Object, "ChangedCluster", "spec", "infraClusterKind", "kind"))
		require.NoError(t, unstructured.SetNestedField(obj.Object, "ChangedClusterTemplate", "spec", "infrastructureTemplateRef", "kind"))
		return
	}
	paths := map[string][]string{
		"provider": {"provider"}, "region": {"region"}, "account": {"account"},
		"infrastructure-template": {"infrastructureTemplateRef", "name"},
		"bootstrap-template":      {"bootstrapTemplateRef", "name"},
		"template-namespace":      {"infrastructureTemplateRef", "namespace"},
	}
	if path, found := paths[change]; found {
		value := "changed"
		require.NoError(t, unstructured.SetNestedField(obj.Object, value, append([]string{"spec"}, path...)...))
		return
	}
	arrays := map[string][]string{
		"zones": {"zones"}, "endpoint": {"endpointPaths"},
		"policy": {"overridePolicy", "infraSpecPaths"},
	}
	value := "changed"
	if change == "endpoint" {
		value = "spec.changedEndpoint"
	}
	if change == "policy" {
		value = "/changed"
	}
	require.NoError(t, unstructured.SetNestedSlice(obj.Object, []any{value}, append([]string{"spec"}, arrays[change]...)...))
}

func TestCompositeProviderAPI_InvalidConfiguration(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		path  []string
		value any
	}{
		{"empty-provider", []string{"provider"}, ""},
		{"invalid-provider", []string{"provider"}, "Not_DNS"},
		{"empty-account", []string{"account"}, ""},
		{"empty-region", []string{"region"}, ""},
		{"missing-infrastructure", []string{"infrastructureTemplateRef"}, map[string]any{}},
		{"source-kind-mismatch", []string{"infrastructureTemplateRef", "kind"}, "OtherClusterTemplate"},
		{"source-group-mismatch", []string{"infrastructureTemplateRef", "apiVersion"}, "other.example.test/v1beta1"},
		{"empty-source-namespace", []string{"infrastructureTemplateRef", "namespace"}, ""},
		{"invalid-source-name", []string{"infrastructureTemplateRef", "name"}, "invalid/name"},
		{"wrong-bootstrap-kind", []string{"bootstrapTemplateRef", "kind"}, "Secret"},
		{"wrong-bootstrap-version", []string{"bootstrapTemplateRef", "apiVersion"}, "bootstrap.cluster.x-k8s.io/v1beta1"},
		{"recursive-profile", []string{"infraClusterKind", "kind"}, "CompositeProvider"},
		{"recursive-cluster", []string{"infraClusterKind", "kind"}, "CompositeCluster"},
		{"recursive-template", []string{"infraClusterKind", "kind"}, "CompositeClusterTemplate"},
		{"bad-pointer", []string{"overridePolicy", "infraSpecPaths"}, []any{"/bad~2escape"}},
		{"wildcard-pointer", []string{"overridePolicy", "infraSpecPaths"}, []any{"/network/*"}},
		{"root-pointer", []string{"overridePolicy", "infraSpecPaths"}, []any{""}},
		{"duplicate-path", []string{"overridePolicy", "infraSpecPaths"}, []any{"/network", "/network"}},
		{"duplicate-zone", []string{"zones"}, []any{"zone-a", "zone-a"}},
		{"invalid-endpoint", []string{"endpointPaths"}, []any{"metadata.name"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			obj := compositeProviderObject()
			require.NoError(t, unstructured.SetNestedField(obj.Object, scenario.value, append([]string{"spec"}, scenario.path...)...))
			err := k8s.Create(t.Context(), obj)
			require.True(t, apierrors.IsInvalid(err), "invalid profile must fail admission: %v", err)
		})
	}
	for _, count := range []int{32, 33} {
		t.Run(fmt.Sprintf("override-paths-%d", count), func(t *testing.T) {
			obj := compositeProviderObject()
			paths := make([]any, count)
			for i := range paths {
				paths[i] = fmt.Sprintf("/field-%d", i)
			}
			require.NoError(t, unstructured.SetNestedSlice(obj.Object, paths, "spec", "overridePolicy", "infraSpecPaths"))
			err := k8s.Create(t.Context(), obj)
			if count == 32 {
				require.NoError(t, err)
			} else {
				require.True(t, apierrors.IsInvalid(err), "%v", err)
			}
		})
	}
}

func TestCompositeProviderAPI_SetOrderDoesNotChangeConfiguration(t *testing.T) {
	obj := compositeProviderObject()
	require.NoError(t, unstructured.SetNestedSlice(obj.Object, []any{"a", "b"}, "spec", "zones"))
	require.NoError(t, unstructured.SetNestedSlice(obj.Object, []any{"/a~1b", "/c~0d"}, "spec", "overridePolicy", "infraSpecPaths"))
	require.NoError(t, k8s.Create(t.Context(), obj))
	require.NoError(t, unstructured.SetNestedSlice(obj.Object, []any{"b", "a"}, "spec", "zones"))
	require.NoError(t, unstructured.SetNestedSlice(obj.Object, []any{"/c~0d", "/a~1b"}, "spec", "overridePolicy", "infraSpecPaths"))
	require.NoError(t, k8s.Update(t.Context(), obj), "schema-defined set reordering is semantically unchanged")
	before := obj.DeepCopy()
	require.NoError(t, unstructured.SetNestedSlice(obj.Object, []any{"/new-path"}, "spec", "overridePolicy", "infraSpecPaths"))
	err := k8s.Update(t.Context(), obj)
	require.True(t, apierrors.IsInvalid(err), "override policy must remain immutable: %v", err)
	require.NoError(t, k8s.Get(t.Context(), client.ObjectKeyFromObject(before), obj))
	assert.Equal(t, before.Object["spec"], obj.Object["spec"])
}

func compositeProviderObject() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"provider": "fake", "account": "account-a", "region": "region-1", "zones": []any{"zone-a"},
		"infraClusterKind": map[string]any{"apiGroup": "infrastructure.cluster.x-k8s.io", "kind": "FakeCluster"},
		"infrastructureTemplateRef": map[string]any{
			"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1",
			"kind":       "FakeClusterTemplate",
			"namespace":  "catalog",
			"name":       "infrastructure-r1",
		},
		"bootstrapTemplateRef": map[string]any{
			"apiVersion": "bootstrap.cluster.x-k8s.io/v1beta2", "kind": "KubeadmConfigTemplate", "namespace": "catalog", "name": "bootstrap-r1",
		},
		"endpointPaths":  []any{"spec.controlPlaneEndpoint"},
		"overridePolicy": map[string]any{"infraSpecPaths": []any{"/network"}, "protectedInfraSpecPaths": []any{"/identityRef"}},
	}}}
	obj.SetGroupVersionKind(infrav1.GroupVersion.WithKind("CompositeProviderTemplate"))
	obj.SetGenerateName("profile-test-")
	return obj
}
