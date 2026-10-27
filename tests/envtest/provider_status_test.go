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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func TestProviderStatus_ProvidersAndNodePools(t *testing.T) {
	template := regionalProfileDefinition()
	template.Spec.Zones = nil
	createRegionalProfile(t, k8s, template)
	protectRegionalProfile(t, template)
	cluster := newCAPICluster(t, newNamespace(t), "worker-owner")
	cluster.Spec.InfrastructureRef.Name = "worker-infrastructure"
	require.NoError(t, k8s.Update(t.Context(), cluster))
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{
		ControlPlaneEndpoint: infrav1.APIEndpoint{Host: "192.0.2.80", Port: 6443},
	})
	paris := addonProvider(t, cc, template.Name, "paris")
	varsovie := addonProvider(t, cc, template.Name, "varsovie", map[string]any{"infraClusterName": "regional-infrastructure"})
	large := addonWorkerPool(t, cc.Namespace, cluster.Name, "large", cc.Name+"-paris")
	small := addonWorkerPool(t, cc.Namespace, cluster.Name, "small", cc.Name+"-paris")
	addonWorkerPool(t, cc.Namespace, cluster.Name, "regional", cc.Name+"-varsovie")
	addonWorkerPool(t, cc.Namespace, "another-owner", "other-cluster", cc.Name+"-paris")
	addonWorkerPool(t, newNamespace(t), cluster.Name, "other-namespace", cc.Name+"-paris")
	wrongKind := addonWorkerPool(t, cc.Namespace, cluster.Name, "other-bootstrap", cc.Name+"-paris")
	require.NoError(t, unstructured.SetNestedField(wrongKind.Object, "ForeignTemplate", "spec", "template", "spec", "bootstrap", "configRef", "kind"))
	require.NoError(t, k8s.Update(t.Context(), wrongKind))
	wrongGroup := addonWorkerPool(t, cc.Namespace, cluster.Name, "other-group", cc.Name+"-paris")
	require.NoError(
		t,
		unstructured.SetNestedField(wrongGroup.Object, "foreign.example.test", "spec", "template", "spec", "bootstrap", "configRef", "apiGroup"),
	)
	require.NoError(t, k8s.Update(t.Context(), wrongGroup))

	r := newClusterReconciler()
	_, cc, err := reconcileCluster(t, r, cc)
	require.NoError(t, err)
	require.Len(t, cc.Status.Providers, 2)
	assert.Equal(t, "paris,varsovie", cc.Status.ProviderNames)
	assert.Equal(t, infrav1.ProviderName("paris"), cc.Status.Providers[0].Name)
	assert.Equal(t, infrav1.ProviderName("varsovie"), cc.Status.Providers[1].Name)
	assertProviderPoolNames(t, &cc.Status.Providers[0], "large", "small")
	assertProviderPoolNames(t, &cc.Status.Providers[1], "regional")
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	for i, p := range []client.Object{paris, varsovie} {
		observed := getAddon(t, p)
		assert.Equal(t, observed.Status.InfrastructureRef, *cc.Status.Providers[i].ChildRef)
		assert.Equal(t, observed.Status.BootstrapTemplateRef, *cc.Status.Providers[i].BootstrapTemplateRef)
		for j := range observed.Status.Conditions {
			expected := observed.Status.Conditions[j]
			actual := cc.Status.Providers[i].Conditions[j]
			expected.LastTransitionTime = actual.LastTransitionTime // The summary owns its transition times.
			assert.Equal(t, expected, actual)
		}
		assert.Equal(t, observed.Status.Profile, cc.Status.Providers[i].Profile)
	}

	markFakeClusterReady(t, cc.Namespace, cc.Name)
	markFakeClusterReady(t, cc.Namespace, "regional-infrastructure")
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	for i := range cc.Status.Providers {
		requireCondition(t, cc.Status.Providers[i].Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
		assert.True(t, *cc.Status.Providers[i].Provisioned)
	}
	version := cc.ResourceVersion
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	assert.Equal(t, version, cc.ResourceVersion, "stable inventories must be write-free")
	require.NoError(t, k8s.Delete(t.Context(), small))
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	assertProviderPoolNames(t, &cc.Status.Providers[0], "large")
	require.NoError(t, k8s.Delete(t.Context(), paris))
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	require.Len(t, cc.Status.Providers, 2, "a draining provider stays visible")
	requireCondition(t, cc.Status.Providers[0].Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.DeletingReason)
	assertProviderPoolNames(t, &cc.Status.Providers[0], "large")
	assert.NotNil(t, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name), "observation does not manage workers or bypass drain")
	assert.NotNil(t, getUnstructured(t, clusterGVK.GroupVersion().WithKind("MachineDeployment"), cc.Namespace, large.GetName()))
}

func TestProviderStatus_UnboundFailureHasNoInventedChildren(t *testing.T) {
	cluster := newCAPICluster(t, newNamespace(t), "pending-provider")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	addonProvider(t, cc, "missing-template", "paris")
	addonWorkerPool(t, cc.Namespace, cluster.Name, "unbound", cc.Name+"-paris")
	_, cc, err := reconcileCluster(t, newClusterReconciler(), cc)
	require.NoError(t, err)
	require.Len(t, cc.Status.Providers, 1)
	p := cc.Status.Providers[0]
	assert.Equal(t, infrav1.ProviderName("paris"), p.Name)
	requireCondition(t, p.Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.ProfileUnavailableReason)
	assert.Nil(t, p.ChildRef)
	assert.Nil(t, p.BootstrapTemplateRef)
	assert.Nil(t, p.Provisioned)
	assert.Empty(t, p.Profile)
	assertProviderPoolNames(t, &p)
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
}

func TestProviderStatus_ClusterDeletionKeepsInventoryCurrent(t *testing.T) {
	for _, deleteOwner := range []bool{false, true} {
		t.Run(map[bool]string{false: "infrastructure", true: "owning-cluster"}[deleteOwner], func(t *testing.T) {
			template := newRegionalProfile(t)
			cluster := newCAPICluster(t, newNamespace(t), "deleting-inventory")
			cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{
				ControlPlaneEndpoint: infrav1.APIEndpoint{Host: "192.0.2.82", Port: 6443},
			})
			addonProvider(t, cc, template.Name, "paris")
			pool := addonWorkerPool(t, cc.Namespace, cluster.Name, "workers", cc.Name+"-paris")
			r := newClusterReconciler()
			_, cc, err := reconcileCluster(t, r, cc)
			require.NoError(t, err)
			markFakeClusterReady(t, cc.Namespace, cc.Name)
			_, cc, err = reconcileCluster(t, r, cc)
			require.NoError(t, err)
			child := getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name)
			child.SetFinalizers([]string{"tests.example.org/hold-native"})
			require.NoError(t, k8s.Update(t.Context(), child))
			if deleteOwner {
				cluster.Finalizers = []string{"tests.example.org/hold-cluster"}
				require.NoError(t, k8s.Update(t.Context(), cluster))
				require.NoError(t, k8s.Delete(t.Context(), cluster))
			} else {
				require.NoError(t, k8s.Delete(t.Context(), cc))
			}
			source := getUnstructured(t, schemaForSource(template.Spec.InfrastructureTemplateRef),
				template.Spec.InfrastructureTemplateRef.Namespace, template.Spec.InfrastructureTemplateRef.Name)
			require.NoError(t, k8s.Delete(t.Context(), source), "teardown observations need no live profile sources")
			_, cc, err = reconcileCluster(t, r, cc)
			require.NoError(t, err)
			require.Len(t, cc.Status.Providers, 1)
			requireCondition(t, cc.Status.Providers[0].Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.DeletingReason)
			assertProviderPoolNames(t, &cc.Status.Providers[0], "workers")
			require.NoError(t, k8s.Delete(t.Context(), pool))
			_, cc, err = reconcileCluster(t, r, cc)
			require.NoError(t, err)
			require.NotNil(t, cc, "native protection must still hold the infrastructure")
			require.Len(t, cc.Status.Providers, 1)
			assertProviderPoolNames(t, &cc.Status.Providers[0])
			requireCondition(t, cc.Status.Providers[0].Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.DeletingReason)
			assert.NotNil(t, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name))
		})
	}
}

func TestProviderStatus_DeletingProviderCannotRemainReady(t *testing.T) {
	template := newRegionalProfile(t)
	cluster := newCAPICluster(t, newNamespace(t), "deleting-provider")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{
		ControlPlaneEndpoint: infrav1.APIEndpoint{Host: "192.0.2.83", Port: 6443},
	})
	p := addonProvider(t, cc, template.Name, "paris")
	r := newClusterReconciler()
	_, cc, err := reconcileCluster(t, r, cc)
	require.NoError(t, err)
	markFakeClusterReady(t, cc.Namespace, cc.Name)
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	child := getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name)
	child.SetFinalizers([]string{"tests.example.org/hold-native"})
	require.NoError(t, k8s.Update(t.Context(), child))
	require.NoError(t, k8s.Delete(t.Context(), p))
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	require.Len(t, cc.Status.Providers, 1)
	requireCondition(t, cc.Status.Providers[0].Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.DeletingReason)
	requireCondition(t, cc.Status.Providers[0].Conditions, infrav1.DeletingCondition, metav1.ConditionTrue, infrav1.DeletingReason)
	assert.True(t, *cc.Status.Providers[0].Provisioned)
	version := cc.ResourceVersion
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	assert.Equal(t, version, cc.ResourceVersion, "drain observations must not churn transition timestamps")
	child = getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name)
	child.SetFinalizers(nil)
	require.NoError(t, k8s.Update(t.Context(), child))
	for range 3 {
		_, cc, err = reconcileCluster(t, r, cc)
		require.NoError(t, err)
	}
	assert.Empty(t, cc.Status.Providers)
	assert.Empty(t, cc.Status.ProviderNames)
}

func assertProviderPoolNames(t *testing.T, provider *infrav1.ProviderStatus, names ...string) {
	t.Helper()
	require.Len(t, provider.NodePools, len(names))
	for i := range provider.NodePools {
		pool := &provider.NodePools[i]
		assert.Equal(t, clusterGVK.GroupVersion().String(), pool.APIVersion)
		assert.Equal(t, "MachineDeployment", pool.Kind)
		assert.Equal(t, names[i], pool.Name)
	}
}
