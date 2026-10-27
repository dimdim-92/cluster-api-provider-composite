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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func testOperatorProviderAddons(t *testing.T, installation *envtest.Environment, c client.Client, binary string) {
	ns := operatorNamespace(t, c, "addons")
	credentials := operatorCredentials(t, installation, c, ns)
	template := regionalProfileDefinition()
	template.Spec.CredentialsNamePaths = []infrav1.ProfileJSONPointer{"/identityRef/name"}
	createRegionalProfile(t, c, template)
	cluster, cc := runtimeClusterWithSpec(t, c, ns, "addon-contract", "addons", infrav1.CompositeClusterSpec{
		ControlPlaneEndpoint: infrav1.APIEndpoint{Host: "192.0.2.50", Port: 6443},
	})
	startOperator(t, binary, credentials, ns, "--watch-filter=addons")
	require.Eventually(t, func() bool {
		current := &infrav1.CompositeCluster{}
		return c.Get(t.Context(), client.ObjectKeyFromObject(cc), current) == nil && current.Status.Initialization.Provisioned != nil &&
			*current.Status.Initialization.Provisioned
	}, 10*time.Second, 100*time.Millisecond)
	p := &infrav1.CompositeProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "supplemental-paris-provider", Namespace: ns},
		Spec: infrav1.CompositeProviderSpec{
			ClusterRef: infrav1.CompositeClusterReference{Name: cc.Name}, TemplateRef: infrav1.CompositeProviderReference{Name: template.Name},
			CredentialsRef: infrav1.ProviderSecretReference{Name: "tenant-credentials"},
		},
	}
	require.NoError(t, c.Create(t.Context(), p))
	child := waitRuntimeObject(t, c, fakeClusterGVK, ns, cc.Name)
	secretName, _, err := unstructured.NestedString(child.Object, "spec", "identityRef", "name")
	require.NoError(t, err)
	assert.Equal(t, "tenant-credentials", secretName, "the real operator injects names without reading Secrets")
	waitRuntimeObject(t, c, kubeadmConfigTemplateGVK, ns, cc.Name+"-"+p.Name)
	markRuntimeChildReady(t, c, child)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(p), p))
	before := p.DeepCopy()
	p.Labels["tests.example.org/observe-native"] = "true"
	// Only this test label changes; concurrent controller status writes must not
	// make the observation trigger conflict or overwrite other metadata.
	require.NoError(t, c.Patch(t.Context(), p, client.MergeFrom(before)))
	require.Eventually(t, func() bool {
		current := &infrav1.CompositeProvider{}
		if c.Get(t.Context(), client.ObjectKeyFromObject(p), current) != nil {
			return false
		}
		ready := condition(current.Status.Conditions, infrav1.ReadyCondition)
		return ready != nil && ready.Status == metav1.ConditionTrue && ready.ObservedGeneration == current.Generation
	}, 10*time.Second, 100*time.Millisecond)
	current := &infrav1.CompositeCluster{}
	// Provider status precedes the cluster snapshot. A retryable conflict can
	// leave the cluster Unknown even after the provider has become Ready.
	require.Eventually(t, func() bool {
		if c.Get(t.Context(), client.ObjectKeyFromObject(cc), current) != nil {
			return false
		}
		ready := condition(current.Status.Conditions, infrav1.ReadyCondition)
		return ready != nil && ready.Status == metav1.ConditionTrue && ready.ObservedGeneration == current.Generation
	}, 10*time.Second, 100*time.Millisecond, "cluster readiness must settle independently of provider readiness")
	requireCondition(t, current.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	testOperatorProviderPoolSummary(t, c, cc, p, cluster.Name)
}

// Simulate a native provider writing status while the real Composite operator
// may still be applying spec/metadata. Refresh on conflicts, not on other errors.
func markRuntimeChildReady(t *testing.T, c client.Client, child *unstructured.Unstructured) {
	t.Helper()
	key := client.ObjectKeyFromObject(child)
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(t.Context(), key, child); err != nil {
			return fmt.Errorf("refreshing runtime native child: %w", err)
		}
		if err := unstructured.SetNestedField(child.Object, true, "status", "ready"); err != nil {
			return fmt.Errorf("setting runtime native readiness: %w", err)
		}
		if err := c.Status().Update(t.Context(), child); err != nil {
			return fmt.Errorf("updating runtime native status: %w", err)
		}
		return nil
	})
	require.NoError(t, err)
}

func testOperatorProviderPoolSummary(t *testing.T, c client.Client, cc *infrav1.CompositeCluster, p *infrav1.CompositeProvider, clusterName string) {
	t.Helper()
	current := &infrav1.CompositeCluster{}
	pool := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": clusterGVK.GroupVersion().String(), "kind": "MachineDeployment",
		"metadata": map[string]any{"name": "native-workers", "namespace": cc.Namespace},
		"spec": map[string]any{
			"clusterName": clusterName, "replicas": int64(1), "selector": map[string]any{},
			"template": map[string]any{"spec": map[string]any{
				"clusterName": clusterName, "version": "v1.34.1",
				"bootstrap": map[string]any{"configRef": map[string]any{
					"apiGroup": kubeadmConfigTemplateGVK.Group, "kind": kubeadmConfigTemplateGVK.Kind, "name": cc.Name + "-" + p.Name,
				}},
				"infrastructureRef": map[string]any{"apiGroup": fakeClusterGVK.Group, "kind": "FakeMachineTemplate", "name": "native-workers"},
			}},
		},
	}}
	require.NoError(t, c.Create(t.Context(), pool))
	require.Eventually(t, func() bool {
		if c.Get(t.Context(), client.ObjectKeyFromObject(cc), current) != nil || len(current.Status.Providers) != 1 {
			return false
		}
		pools := current.Status.Providers[0].NodePools
		ready := condition(current.Status.Conditions, infrav1.ReadyCondition)
		return len(pools) == 1 && pools[0].Name == pool.GetName() &&
			ready != nil && ready.Status == metav1.ConditionTrue && ready.ObservedGeneration == current.Generation
	}, 10*time.Second, 100*time.Millisecond, "worker creation must refresh the summary without waiting for polling")
	assertProviderPoolNames(t, &current.Status.Providers[0], pool.GetName())
	requireCondition(t, current.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	require.NoError(t, c.Delete(t.Context(), pool))
	require.Eventually(t, func() bool {
		return c.Get(t.Context(), client.ObjectKeyFromObject(cc), current) == nil && len(current.Status.Providers) == 1 &&
			len(current.Status.Providers[0].NodePools) == 0
	}, 10*time.Second, 100*time.Millisecond, "worker deletion must remove stale pool references without waiting for polling")
}
