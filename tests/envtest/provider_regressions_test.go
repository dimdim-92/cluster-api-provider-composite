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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
)

func TestProviderRegressions_BootstrapMergeAndSSA(t *testing.T) {
	for _, priorApply := range []bool{false, true} {
		for _, change := range []string{"null", "named-list", "empty-array"} {
			t.Run(fmt.Sprintf("%s/prior-apply=%t", change, priorApply), func(t *testing.T) {
				template := regionalProfileDefinition()
				template.Spec.OverridePolicy.BootstrapPaths = []infrav1.ProfileJSONPointer{"/files", "/preKubeadmCommands", "/joinConfiguration"}
				createRegionalProfile(t, k8s, template)
				protectRegionalProfile(t, template)
				cluster := newCAPICluster(t, newNamespace(t), "bootstrap")
				cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{Bootstrap: &apiextensionsv1JSON{Raw: []byte(`{
					"preKubeadmCommands":["echo shared"],
					"files":[{"path":"/etc/shared","content":"shared"}],
					"joinConfiguration":{"nodeRegistration":{"name":"created-node","taints":[{"key":"pool","effect":"NoSchedule"}],"kubeletExtraArgs":[
						{"name":"cloud-provider","value":"external"},{"name":"node-labels","value":"pool=old"}]}}
				}`)}})
				setEndpoint(t, cc, cluster, "192.0.2.70", 6443)
				p := addonProvider(t, cc, template.Name, "paris")
				r := newClusterReconciler()
				_, cc, err := reconcileCluster(t, r, cc)
				require.NoError(t, err)
				created := getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, cc.Name+"-paris")
				require.NotNil(t, created, "exercise atomic Create before SSA updates")
				actorFields := applyBootstrapRegressionActor(t, created, priorApply)

				// Provider specs are immutable: only the shared bootstrap input changes.
				payload := bootstrapRegressionPayload(change)
				cc.Spec.Bootstrap = &apiextensionsv1JSON{Raw: []byte(payload)}
				require.NoError(t, k8s.Update(t.Context(), cc))
				markFakeClusterReady(t, cc.Namespace, cc.Name)
				_, cc, err = reconcileCluster(t, r, cc)
				require.NoError(t, err)
				requireCondition(t, getAddon(t, p).Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
				updated := getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, created.GetName())
				require.NotNil(t, updated)
				assertBootstrapRegressionUpdate(t, created, updated, actorFields, change)
				for range 3 {
					_, cc, err = reconcileCluster(t, r, cc)
					require.NoError(t, err)
					assert.Equal(
						t,
						updated.GetResourceVersion(),
						getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, created.GetName()).GetResourceVersion(),
					)
				}
			})
		}
	}
}

func applyBootstrapRegressionActor(t *testing.T, created *unstructured.Unstructured, priorApply bool) []metav1.ManagedFieldsEntry {
	t.Helper()
	if priorApply {
		previous := profileSourceObject(infrav1.ProviderTemplateReference{
			APIVersion: kubeadmConfigTemplateGVK.GroupVersion().String(), Kind: kubeadmConfigTemplateGVK.Kind,
			Namespace: created.GetNamespace(), Name: created.GetName(),
		}, nil)
		previous.Object["spec"] = created.DeepCopy().Object["spec"]
		previous.SetLabels(created.GetLabels())
		previous.SetOwnerReferences(created.GetOwnerReferences())
		require.NoError(t, k8s.Apply(t.Context(), client.ApplyConfigurationFromUnstructured(previous), capi.FieldOwner, client.ForceOwnership))
	}
	actor := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"verbosity": int64(3)}}},
	}}
	actor.SetGroupVersionKind(kubeadmConfigTemplateGVK)
	actor.SetName(created.GetName())
	actor.SetNamespace(created.GetNamespace())
	actor.SetAnnotations(map[string]string{"tests.example.org/actor": "preserve"})
	require.NoError(t, k8s.Apply(t.Context(), client.ApplyConfigurationFromUnstructured(actor), client.FieldOwner("bootstrap-other-actor")))
	var actorFields []metav1.ManagedFieldsEntry
	operations := map[metav1.ManagedFieldsOperationType]bool{}
	for _, entry := range actor.GetManagedFields() {
		if entry.Manager == "bootstrap-other-actor" {
			actorFields = append(actorFields, entry)
		}
		if entry.Manager == string(capi.FieldOwner) && entry.Subresource == "" {
			operations[entry.Operation] = true
		}
	}
	require.NotEmpty(t, actorFields)
	require.True(t, operations[metav1.ManagedFieldsOperationUpdate])
	require.Equal(t, priorApply, operations[metav1.ManagedFieldsOperationApply])
	return actorFields
}

func bootstrapRegressionPayload(change string) string {
	switch change {
	case "named-list":
		return `{"preKubeadmCommands":["echo replacement"],"joinConfiguration":{"nodeRegistration":{"kubeletExtraArgs":[{"name":"cloud-provider","value":"external"},{"name":"node-labels","value":"pool=new"},{"name":"provider-id","value":"shared-must-lose"}]}}}`
	case "empty-array":
		return `{"joinConfiguration":{"nodeRegistration":{"taints":[],"kubeletExtraArgs":[]}}}`
	default:
		return `{"files":null,"joinConfiguration":{"nodeRegistration":{"name":null,"kubeletExtraArgs":[{"name":"cloud-provider","value":"external"},{"name":"node-labels","value":"pool=old"}]}}}`
	}
}

func assertBootstrapRegressionUpdate(
	t *testing.T,
	created, updated *unstructured.Unstructured,
	actorFields []metav1.ManagedFieldsEntry,
	change string,
) {
	t.Helper()
	assert.Equal(t, created.GetUID(), updated.GetUID())
	assert.Equal(t, created.GetOwnerReferences(), updated.GetOwnerReferences())
	assert.Equal(t, "preserve", updated.GetAnnotations()["tests.example.org/actor"])
	verbosity, _, err := unstructured.NestedInt64(updated.Object, "spec", "template", "spec", "verbosity")
	require.NoError(t, err)
	assert.Equal(t, int64(3), verbosity)
	for _, entry := range actorFields {
		assert.Contains(t, updated.GetManagedFields(), entry)
	}
	if change == "null" {
		for _, path := range [][]string{{"files"}, {"joinConfiguration", "nodeRegistration", "name"}} {
			_, found, err := unstructured.NestedFieldNoCopy(updated.Object, append([]string{"spec", "template", "spec"}, path...)...)
			require.NoError(t, err)
			assert.False(t, found, "null must remove a field originally owned by Create")
		}
	} else {
		commands, _, err := unstructured.NestedSlice(updated.Object, "spec", "template", "spec", "preKubeadmCommands")
		require.NoError(t, err)
		if change == "named-list" {
			assert.Equal(t, []any{"echo replacement"}, commands)
		} else {
			assert.Empty(t, commands)
			taints, found, err := unstructured.NestedSlice(
				updated.Object,
				"spec",
				"template",
				"spec",
				"joinConfiguration",
				"nodeRegistration",
				"taints",
			)
			require.NoError(t, err)
			require.True(t, found, "empty taints is meaningful to kubeadm")
			assert.Empty(t, taints)
		}
	}
	args, _, err := unstructured.NestedSlice(updated.Object, "spec", "template", "spec", "joinConfiguration", "nodeRegistration", "kubeletExtraArgs")
	require.NoError(t, err)
	want := []any{map[string]any{"name": "provider-id", "value": "fake:///native"}}
	if change != "empty-array" {
		value := "pool=old"
		if change == "named-list" {
			value = "pool=new"
		}
		want = append(want, map[string]any{"name": "cloud-provider", "value": "external"}, map[string]any{"name": "node-labels", "value": value})
	}
	assert.ElementsMatch(t, want, args, "raw list comparison catches duplicate stale named entries")
}

func TestProviderRegressions_SourceUIDPins(t *testing.T) {
	for _, bootstrap := range []bool{false, true} {
		t.Run(map[bool]string{false: "infrastructure", true: "bootstrap"}[bootstrap], func(t *testing.T) {
			template := newRegionalProfile(t)
			cluster := newCAPICluster(t, newNamespace(t), "source-pins")
			cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
			setEndpoint(t, cc, cluster, "192.0.2.71", 6443)
			p := addonProvider(t, cc, template.Name, "paris")
			r := newClusterReconciler()
			_, cc, err := reconcileCluster(t, r, cc)
			require.NoError(t, err)
			markFakeClusterReady(t, cc.Namespace, cc.Name)
			_, cc, err = reconcileCluster(t, r, cc)
			require.NoError(t, err)
			ref := template.Spec.InfrastructureTemplateRef
			if bootstrap {
				ref = template.Spec.BootstrapTemplateRef
			}
			source := getUnstructured(t, schemaForSource(ref), ref.Namespace, ref.Name)
			require.NotNil(t, source)
			generation := source.GetGeneration()
			source.SetLabels(map[string]string{"tests.example.org/edit": "allowed"})
			source.SetAnnotations(map[string]string{"tests.example.org/edit": "allowed"})
			require.NoError(t, k8s.Update(t.Context(), source))
			assert.Equal(t, generation, source.GetGeneration())
			_, cc, err = reconcileCluster(t, r, cc)
			require.NoError(t, err)
			requireCondition(t, getAddon(t, p).Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
			infra := getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name)
			kct := getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, cc.Name+"-paris")
			clusterPin := cc.Annotations[infrav1.ProviderAttachmentsAnnotation]
			providerPin := getAddon(t, p).Annotations[infrav1.ProviderBindingAnnotation]
			require.NotEmpty(t, clusterPin)
			require.NotEmpty(t, providerPin)
			require.NoError(t, k8s.Delete(t.Context(), source))
			replacement := profileSourceObject(ref, nil)
			replacement.Object["spec"] = source.DeepCopy().Object["spec"]
			require.NoError(t, k8s.Create(t.Context(), replacement))
			assert.NotEqual(t, source.GetUID(), replacement.GetUID())
			assert.Equal(t, source.Object["spec"], replacement.Object["spec"], "identical payload cannot authorize a new source UID")
			assert.Equal(t, generation, replacement.GetGeneration())
			_, cc, err = reconcileCluster(t, r, cc)
			require.NoError(t, err)
			requireCondition(t, getAddon(t, p).Status.Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.ProfileMismatchReason)
			assert.Equal(t, clusterPin, cc.Annotations[infrav1.ProviderAttachmentsAnnotation])
			assert.Equal(t, providerPin, getAddon(t, p).Annotations[infrav1.ProviderBindingAnnotation])
			assert.Equal(t, infra.Object, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name).Object)
			assert.Equal(t, kct.Object, getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, cc.Name+"-paris").Object)
		})
	}
}

func TestProviderRegressions_MissingPinsCannotReconstruct(t *testing.T) {
	for _, lost := range []string{"cluster", "provider", "both"} {
		t.Run(lost, func(t *testing.T) {
			template := newRegionalProfile(t)
			cluster := newCAPICluster(t, newNamespace(t), "missing-pins")
			cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
			setEndpoint(t, cc, cluster, "192.0.2.72", 6443)
			p := addonProvider(t, cc, template.Name, "paris")
			r := newClusterReconciler()
			_, cc, err := reconcileCluster(t, r, cc)
			require.NoError(t, err)
			infra := getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name)
			kct := getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, cc.Name+"-paris")
			if lost != "provider" {
				delete(cc.Annotations, infrav1.ProviderAttachmentsAnnotation)
				require.NoError(t, k8s.Update(t.Context(), cc))
			}
			if lost != "cluster" {
				fresh := getAddon(t, p)
				delete(fresh.Annotations, infrav1.ProviderBindingAnnotation)
				require.NoError(t, k8s.Update(t.Context(), fresh))
			}
			_, cc, err = reconcileCluster(t, r, cc)
			require.NoError(t, err)
			requireCondition(t, getAddon(t, p).Status.Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.ProfileMismatchReason)
			if lost != "provider" {
				assert.Empty(t, cc.Annotations[infrav1.ProviderAttachmentsAnnotation])
			}
			if lost != "cluster" {
				assert.Empty(t, getAddon(t, p).Annotations[infrav1.ProviderBindingAnnotation])
			}
			assert.Equal(t, infra.Object, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name).Object)
			assert.Equal(t, kct.Object, getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, cc.Name+"-paris").Object)
		})
	}
}

func TestProviderRegressions_OwnerNameDiffersFromInfrastructureName(t *testing.T) {
	template := newRegionalProfile(t)
	cluster := newCAPICluster(t, newNamespace(t), "actual-owner")
	cluster.Spec.InfrastructureRef.Name = "canonical-infrastructure"
	require.NoError(t, k8s.Update(t.Context(), cluster))
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	setEndpoint(t, cc, cluster, "192.0.2.73", 6443)
	p := addonProvider(t, cc, template.Name, "paris")
	_, cc, err := reconcileCluster(t, newClusterReconciler(), cc)
	require.NoError(t, err)
	for _, child := range []*unstructured.Unstructured{
		getUnstructured(t, fakeClusterGVK, cc.Namespace, "canonical-infrastructure"),
		getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, "canonical-infrastructure-paris"),
	} {
		require.NotNil(t, child)
		assert.Equal(t, cluster.Name, child.GetLabels()["cluster.x-k8s.io/cluster-name"])
		owner := metav1.GetControllerOf(child)
		require.NotNil(t, owner)
		assert.Equal(t, cc.UID, owner.UID)
		assert.Equal(t, cc.Name, owner.Name)
		assert.Contains(t, child.GetOwnerReferences(), metav1.OwnerReference{
			APIVersion: clusterGVK.GroupVersion().String(), Kind: "Cluster", Name: cluster.Name, UID: cluster.UID,
		})
	}
	assert.Equal(t, "canonical-infrastructure-paris", getAddon(t, p).Status.BootstrapTemplateRef.Name)
	assert.Nil(t, getUnstructured(t, fakeClusterGVK, cc.Namespace, cluster.Name))
	assert.Nil(t, getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, cluster.Name+"-paris"))
}
