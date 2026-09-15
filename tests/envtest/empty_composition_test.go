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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func TestEmptyComposition_ReadyWithoutEndpointOrNativeChildren(t *testing.T) {
	cluster := newCAPICluster(t, newNamespace(t), "empty")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	r := newClusterReconciler()
	result, cc, err := reconcileCluster(t, r, cc)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, result.RequeueAfter)
	for _, typ := range []string{infrav1.ReadyCondition, infrav1.ProvidersReadyCondition, infrav1.EndpointPropagatedCondition, infrav1.BootstrapTemplatesReadyCondition} {
		requireCondition(t, cc.Status.Conditions, typ, metav1.ConditionTrue, infrav1.ReadyReason)
		assert.Equal(t, cc.Generation, condition(cc.Status.Conditions, typ).ObservedGeneration)
	}
	require.NotNil(t, cc.Status.Initialization.Provisioned)
	assert.True(t, *cc.Status.Initialization.Provisioned)
	assert.True(t, cc.Spec.ControlPlaneEndpoint.IsZero())
	assert.Empty(t, cc.Status.Providers)
	assert.Empty(t, cc.Status.ProviderNames)
	assert.Empty(t, cc.Status.FailureDomains)
	assert.Contains(t, cc.Finalizers, infrav1.ClusterFinalizer)
	assert.Empty(t, cc.Annotations["compositecluster.infrastructure.cluster.x-k8s.io/provider-bindings"])
	for _, gvk := range []string{"FakeClusterList", "KubeadmConfigTemplateList"} {
		list := &unstructured.UnstructuredList{}
		version := fakeClusterGVK.GroupVersion()
		if gvk == "KubeadmConfigTemplateList" {
			version = kubeadmConfigTemplateGVK.GroupVersion()
		}
		list.SetGroupVersionKind(version.WithKind(gvk))
		require.NoError(t, k8s.List(t.Context(), list, client.InNamespace(cc.Namespace)))
		assert.Empty(t, list.Items)
	}
	version := cc.ResourceVersion
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	assert.Equal(t, version, cc.ResourceVersion, "settled empty infrastructure must not churn status")
	deleteProfileConsumer(t, r, cc)
}

func TestEmptyComposition_AttachLaterAndRetainInitialization(t *testing.T) {
	cluster := newCAPICluster(t, newNamespace(t), "append-later")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	r := newClusterReconciler()
	_, cc, err := reconcileCluster(t, r, cc)
	require.NoError(t, err)
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	p := addonProvider(t, cc, "late-profile", "regional")
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	requireCondition(t, getAddon(t, p).Status.Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.ProfileUnavailableReason)
	assert.True(t, *cc.Status.Initialization.Provisioned)
	template := regionalProfileDefinition()
	template.Name, template.GenerateName = "late-profile", ""
	createRegionalProfile(t, k8s, template)
	protectRegionalProfile(t, template)
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	require.NotNil(t, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name))
	assert.Nil(t, getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, cc.Name+"-regional"))
	assert.True(t, *cc.Status.Initialization.Provisioned)
	setEndpoint(t, cc, cluster, "192.0.2.30", 6443)
	markFakeClusterReady(t, cc.Namespace, cc.Name)
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	requireCondition(t, getAddon(t, p).Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	assert.Equal(t, cc.Generation, cc.Status.ObservedGeneration)
	deleteProfileConsumer(t, r, cc)
}

func TestEmptyComposition_RequiresOwnerAndHonorsPause(t *testing.T) {
	cc := &infrav1.CompositeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "unowned", Namespace: newNamespace(t)},
		Spec:       infrav1.CompositeClusterSpec{},
	}
	require.NoError(t, k8s.Create(t.Context(), cc))
	_, cc, err := reconcileCluster(t, newClusterReconciler(), cc)
	require.NoError(t, err)
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.WaitingForClusterReason)
	assert.Empty(t, cc.Finalizers)
	assert.Nil(t, cc.Status.Initialization.Provisioned)
	cluster := newCAPICluster(t, newNamespace(t), "paused-empty")
	paused := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	paused.Annotations = map[string]string{"cluster.x-k8s.io/paused": ""}
	require.NoError(t, k8s.Update(t.Context(), paused))
	_, paused, err = reconcileCluster(t, newClusterReconciler(), paused)
	require.NoError(t, err)
	requireCondition(t, paused.Status.Conditions, infrav1.PausedCondition, metav1.ConditionTrue, infrav1.PausedReason)
	assert.Nil(t, paused.Status.Initialization.Provisioned)
	assert.Empty(t, paused.Finalizers)
}

func TestEmptyComposition_TemplateAdmission(t *testing.T) {
	template := &infrav1.CompositeClusterTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: newNamespace(t)},
		Spec: infrav1.CompositeClusterTemplateSpec{Template: infrav1.CompositeClusterTemplateResource{
			Spec: infrav1.CompositeClusterSpec{},
		}},
	}
	require.NoError(t, k8s.Create(t.Context(), template))
	require.NoError(t, k8s.Get(t.Context(), client.ObjectKeyFromObject(template), template))
	assert.Equal(t, infrav1.CompositeClusterSpec{}, template.Spec.Template.Spec)
	template.Spec.Template.Spec.ControlPlaneEndpoint = infrav1.APIEndpoint{Host: "192.0.2.1", Port: 6443}
	require.True(t, apierrors.IsInvalid(k8s.Update(t.Context(), template)), "empty templates remain immutable")
}
