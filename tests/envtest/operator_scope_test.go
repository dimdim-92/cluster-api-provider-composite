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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	eventsv1 "k8s.io/api/events/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func testOperatorScope(t *testing.T, installation *envtest.Environment, c client.Client, binary string) {
	ns := operatorNamespace(t, c, "scope")
	outside := operatorNamespace(t, c, "outside")
	credentials := operatorCredentials(t, installation, c, ns)
	cluster, active := runtimeCluster(t, c, ns, "active", "scope")
	_, otherNS := runtimeCluster(t, c, outside, "outside", "scope")
	_, otherFilter := runtimeCluster(t, c, ns, "filtered", "another")
	_, managed := runtimeCluster(t, c, ns, "managed", "scope")
	managed.Labels[clusterv1.ManagedByAnnotation] = "external"
	require.NoError(t, c.Update(t.Context(), managed))
	_, annotated := runtimeCluster(t, c, ns, "annotated", "scope")
	annotated.Annotations = map[string]string{clusterv1.ManagedByAnnotation: "external"}
	require.NoError(t, c.Update(t.Context(), annotated))
	_, paused := runtimeCluster(t, c, ns, "paused", "scope")
	paused.Annotations = map[string]string{clusterv1.PausedAnnotation: ""}
	require.NoError(t, c.Update(t.Context(), paused))
	startOperator(t, binary, credentials, ns, "--watch-filter=scope")
	waitRuntimeObject(t, c, fakeClusterGVK, ns, active.Name)
	require.Eventually(t, func() bool {
		events := &eventsv1.EventList{}
		require.NoError(t, c.List(t.Context(), events, client.InNamespace(ns)))
		for i := range events.Items {
			event := &events.Items[i]
			if event.Regarding.UID == active.UID && event.Reason == "Ready" {
				return event.Action == "Reconcile" && event.ReportingController == "capi-composite" && event.Note != ""
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "the shipped role must permit events.k8s.io/v1 Events with reconciliation metadata")
	for _, excluded := range []*infrav1.CompositeCluster{otherNS, otherFilter, managed, annotated} {
		require.Never(t, func() bool {
			current := &infrav1.CompositeCluster{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(excluded), current))
			return current.ResourceVersion != excluded.ResourceVersion
		}, time.Second, 50*time.Millisecond, "excluded resource received writes")
		assertRuntimeAbsent(t, c, fakeClusterGVK, excluded.Namespace, excluded.Name)
		assertRuntimeAbsent(t, c, kubeadmConfigTemplateGVK, excluded.Namespace, excluded.Name+"-"+excluded.Name+"-fake")
	}
	assertRuntimeAbsent(t, c, fakeClusterGVK, ns, paused.Name)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(paused), paused))
	delete(paused.Annotations, clusterv1.PausedAnnotation)
	require.NoError(t, c.Update(t.Context(), paused))
	waitRuntimeObject(t, c, fakeClusterGVK, ns, paused.Name)

	// Cluster spec changes must route through the real watch, not the 30s poll.
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "203.0.113.91", Port: 6443}
	require.NoError(t, c.Update(t.Context(), cluster))
	waitRuntimeObject(t, c, kubeadmConfigTemplateGVK, ns, active.Name+"-"+active.Name+"-fake")
	child := waitRuntimeObject(t, c, fakeClusterGVK, ns, active.Name)
	host, _, err := unstructured.NestedString(child.Object, "spec", "controlPlaneEndpoint", "host")
	require.NoError(t, err)
	assert.Equal(t, cluster.Spec.ControlPlaneEndpoint.Host, host)

	// Deletion changes metadata, not generation. It must wake reconciliation promptly.
	child.SetFinalizers([]string{"test/provider-hold"})
	require.NoError(t, c.Update(t.Context(), child))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(active), active))
	require.NoError(t, c.Delete(t.Context(), active))
	require.Eventually(t, func() bool {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(fakeClusterGVK)
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(child), live))
		return live.GetDeletionTimestamp() != nil
	}, 10*time.Second, 100*time.Millisecond)
	template := waitRuntimeObject(t, c, kubeadmConfigTemplateGVK, ns, active.Name+"-"+active.Name+"-fake")
	assert.Nil(t, template.GetDeletionTimestamp(), "bootstrap must remain while infrastructure deletion is held")
	child = waitRuntimeObject(t, c, fakeClusterGVK, ns, active.Name)
	child.SetFinalizers(nil)
	require.NoError(t, c.Update(t.Context(), child))
	// Owned-template updates wake the controller; dynamic children deliberately poll.
	template.SetAnnotations(map[string]string{"test/wake": "deletion"})
	require.NoError(t, c.Update(t.Context(), template))
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(c.Get(t.Context(), client.ObjectKeyFromObject(active), &infrav1.CompositeCluster{}))
	}, 40*time.Second, 100*time.Millisecond)
	assertRuntimeAbsent(t, c, kubeadmConfigTemplateGVK, ns, template.GetName())
}

func testOperatorAllNamespaces(t *testing.T, installation *envtest.Environment, c client.Client, binary string) {
	first, second := operatorNamespace(t, c, "all-a"), operatorNamespace(t, c, "all-b")
	credentials := operatorCredentials(t, installation, c, first)
	_, a := runtimeCluster(t, c, first, "all", "all")
	_, b := runtimeCluster(t, c, second, "all", "all")
	startOperator(t, binary, credentials, "", "--watch-filter=all")
	waitRuntimeObject(t, c, fakeClusterGVK, first, a.Name)
	waitRuntimeObject(t, c, fakeClusterGVK, second, b.Name)
}

func testOperatorLeadership(t *testing.T, installation *envtest.Environment, c client.Client, binary string) {
	ns := operatorNamespace(t, c, "leadership")
	leases := operatorNamespace(t, c, "leases")
	credentials := operatorCredentials(t, installation, c, ns)
	args := []string{
		"--leader-elect=true", "--leader-election-id=runtime-election", "--leader-election-namespace=" + leases,
		"--leader-elect-lease-duration=5s", "--leader-elect-renew-deadline=3s", "--leader-elect-retry-period=500ms", "--watch-filter=election",
	}
	first := startOperator(t, binary, credentials, ns, args...)
	key := client.ObjectKey{Namespace: leases, Name: "runtime-election"}
	lease := &coordinationv1.Lease{}
	require.Eventually(t, func() bool {
		return c.Get(t.Context(), key, lease) == nil && lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != ""
	}, 10*time.Second, 100*time.Millisecond)
	holder := *lease.Spec.HolderIdentity
	second := startOperator(t, binary, credentials, ns, args...)
	_, active := runtimeCluster(t, c, ns, "first-leader", "election")
	waitRuntimeObject(t, c, fakeClusterGVK, ns, active.Name)
	require.NoError(t, c.Get(t.Context(), key, lease))
	assert.Equal(t, holder, *lease.Spec.HolderIdentity)
	assert.NotContains(t, second.output.String(), "Composite state changed", "standby must not reconcile")
	stopOperator(t, first)
	require.Eventually(t, func() bool {
		return c.Get(t.Context(), key, lease) == nil && lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != holder
	}, 15*time.Second, 100*time.Millisecond)
	_, next := runtimeCluster(t, c, ns, "next-leader", "election")
	waitRuntimeObject(t, c, fakeClusterGVK, ns, next.Name)
	require.Eventually(t, func() bool { return strings.Contains(second.output.String(), "Composite state changed") },
		10*time.Second, 100*time.Millisecond, "the new leader must finish status persistence after creating its child")
}

func runtimeCluster(t *testing.T, c client.Client, namespace, name, filter string) (*clusterv1.Cluster, *infrav1.CompositeCluster) {
	t.Helper()
	cluster, cc := runtimeClusterWithSpec(t, c, namespace, name, filter, infrav1.CompositeClusterSpec{})
	template := regionalProfileDefinition()
	createRegionalProfile(t, c, template)
	runtimeProvider(t, c, cc, template.Name, "fake")
	return cluster, cc
}

func runtimeClusterWithSpec(
	t *testing.T,
	c client.Client,
	namespace, name, filter string,
	spec infrav1.CompositeClusterSpec,
) (*clusterv1.Cluster, *infrav1.CompositeCluster) {
	t.Helper()
	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}, Spec: clusterv1.ClusterSpec{
		InfrastructureRef: clusterv1.ContractVersionedObjectReference{APIGroup: infrav1.GroupVersion.Group, Kind: "CompositeCluster", Name: name},
	}}
	require.NoError(t, c.Create(t.Context(), cluster))
	cc := &infrav1.CompositeCluster{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace, Name: name, Labels: map[string]string{clusterv1.WatchLabel: filter},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cluster, clusterGVK)},
	}, Spec: spec}
	require.NoError(t, c.Create(t.Context(), cc))
	return cluster, cc
}

func testOperatorCustomGroup(t *testing.T, installation *envtest.Environment, c client.Client, binary string) {
	ns := operatorNamespace(t, c, "custom-group")
	crd := compositionCRD(t, ns)
	crd.Spec.Group = ns + ".example.test"
	crd.Name = crd.Spec.Names.Plural + "." + crd.Spec.Group
	sourceCRD := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: "fakeclustertemplates.infrastructure.cluster.x-k8s.io"}, sourceCRD))
	sourceCRD.ObjectMeta = metav1.ObjectMeta{Name: crd.Spec.Names.Plural + "templates." + crd.Spec.Group}
	sourceCRD.Spec.Group = crd.Spec.Group
	sourceCRD.Spec.Names = apiextensionsv1.CustomResourceDefinitionNames{
		Kind:     crd.Spec.Names.Kind + "Template",
		ListKind: crd.Spec.Names.Kind + "TemplateList",
		Plural:   crd.Spec.Names.Plural + "templates",
	}
	sourceCRD.Status = apiextensionsv1.CustomResourceDefinitionStatus{}
	_, err := envtest.InstallCRDs(installation.Config, envtest.CRDInstallOptions{CRDs: []*apiextensionsv1.CustomResourceDefinition{crd, sourceCRD}})
	require.NoError(t, err)
	credentials := operatorCredentials(t, installation, c, ns)
	_, cc := runtimeClusterWithSpec(t, c, ns, "custom", "custom", infrav1.CompositeClusterSpec{})
	template := regionalProfileDefinition()
	template.Spec.InfraClusterKind = infrav1.NativeInfraClusterKind{APIGroup: crd.Spec.Group, Kind: crd.Spec.Names.Kind}
	// Sources remain administrator-owned; the shipped role deliberately lacks this native group.
	template.Spec.InfrastructureTemplateRef = infrav1.ProviderTemplateReference{
		APIVersion: crd.Spec.Group + "/v1beta1",
		Kind:       crd.Spec.Names.Kind + "Template",
		Namespace:  ns,
		Name:       "source",
	}
	template.Spec.BootstrapTemplateRef.Namespace = ns
	createProfileSourceTemplates(t, c, template)
	require.NoError(t, c.Create(t.Context(), template))
	runtimeProvider(t, c, cc, template.Name, "fake")
	p := startOperator(t, binary, credentials, ns, "--watch-filter=custom")
	require.Eventually(t, func() bool { return strings.Contains(p.output.String(), "RBAC") },
		10*time.Second, 100*time.Millisecond, "missing useful forbidden diagnostic")
	current := &infrav1.CompositeCluster{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cc), current))
	assert.Empty(t, current.Annotations[infrav1.ProviderAttachmentsAnnotation])
	gvk := schema.GroupVersionKind{Group: crd.Spec.Group, Version: "v1beta1", Kind: crd.Spec.Names.Kind}
	assertRuntimeAbsent(t, c, gvk, ns, cc.Name)
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "custom-provider", Namespace: ns}, Rules: []rbacv1.PolicyRule{
		{
			APIGroups: []string{
				crd.Spec.Group,
			},
			Resources: []string{crd.Spec.Names.Plural, sourceCRD.Spec.Names.Plural},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		},
	}}
	require.NoError(t, c.Create(t.Context(), role))
	require.NoError(t, c.Create(t.Context(), &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: "operator-" + ns}},
	}))
	waitRuntimeObject(t, c, gvk, ns, cc.Name)
	assertRuntimeAbsent(t, c, fakeClusterGVK, ns, cc.Name)
}

func waitRuntimeObject(t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	require.Eventually(t, func() bool {
		return c.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: name}, obj) == nil
	}, 10*time.Second, 100*time.Millisecond, "manager did not reconcile %s %s/%s", gvk.Kind, namespace, name)
	return obj
}

func testOperatorOwnerEventGroup(t *testing.T, installation *envtest.Environment, c client.Client, binary string) {
	ns := operatorNamespace(t, c, "routing")
	credentials := operatorCredentials(t, installation, c, ns)
	cluster, cc := runtimeCluster(t, c, ns, "routing", "routing")
	cc.OwnerReferences = nil
	require.NoError(t, c.Update(t.Context(), cc))
	cluster.Spec.InfrastructureRef.APIGroup = "other.example.test"
	require.NoError(t, c.Update(t.Context(), cluster))
	p := startOperator(t, binary, credentials, ns, "--watch-filter=routing")
	require.Eventually(t, func() bool {
		current := &infrav1.CompositeCluster{}
		return c.Get(t.Context(), client.ObjectKeyFromObject(cc), current) == nil && current.Status.Reason == infrav1.WaitingForClusterReason
	}, 10*time.Second, 100*time.Millisecond)
	const marker = "Reconciling CompositeCluster"
	last, stable := 0, 0
	require.Eventually(t, func() bool {
		count := strings.Count(p.output.String(), marker)
		if count == last {
			stable++
		} else {
			last, stable = count, 0
		}
		return last > 0 && stable >= 3
	}, 5*time.Second, 100*time.Millisecond)
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "203.0.113.92", Port: 6443}
	require.NoError(t, c.Update(t.Context(), cluster))
	require.Never(t, func() bool { return strings.Count(p.output.String(), marker) > last }, 2*time.Second, 100*time.Millisecond,
		"an infrastructureRef in another API group must not enqueue Composite")
	cluster.Spec.InfrastructureRef.APIGroup = infrav1.GroupVersion.Group
	require.NoError(t, c.Update(t.Context(), cluster))
	require.Eventually(t, func() bool { return strings.Count(p.output.String(), marker) > last }, 5*time.Second, 100*time.Millisecond,
		"the positive-control matching API group must enqueue Composite")
	assertRuntimeAbsent(t, c, fakeClusterGVK, ns, cc.Name)
}

func assertRuntimeAbsent(t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string) {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	require.True(t, apierrors.IsNotFound(c.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: name}, obj)), "%s must not exist", name)
}
