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

package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func TestProviderStatusReplacementCannotInheritBindingOrPools(t *testing.T) {
	cc, bindings := attachmentBindingsFixture()
	p := infrav1.CompositeProvider{ObjectMeta: metav1.ObjectMeta{Name: "regional", UID: "replacement", Generation: 1}}
	p.Status.InfrastructureRef = infrav1.ChildReference{APIVersion: "infra.test/v1beta1", Kind: "FakeCluster", Name: cc.Name}
	p.Status.BootstrapTemplateRef = infrav1.ChildReference{
		APIVersion: kubeadmConfigTemplateGVK.GroupVersion().String(),
		Kind:       kubeadmConfigTemplateGVK.Kind,
		Name:       cc.Name + "-regional",
	}
	p.Status.Initialization.Provisioned = new(true)
	cc.Status.Providers = []infrav1.ProviderStatus{{Name: "regional", Profile: bindings.Providers["regional"].Binding.profileStatus()}}
	statuses, owners, err := projectProviderStatuses(cc, []infrav1.CompositeProvider{p}, bindings, false)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Empty(t, statuses[0].Profile)
	assert.Nil(t, statuses[0].ChildRef)
	assert.Nil(t, statuses[0].BootstrapTemplateRef)
	assert.Nil(t, statuses[0].Provisioned)
	assert.Empty(t, owners)
}

func TestProviderStatusKeepsDrainingIdentityAndDropsCompletedRetirement(t *testing.T) {
	cc, bindings := attachmentBindingsFixture()
	statuses, owners, err := projectProviderStatuses(cc, nil, bindings, false)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, infrav1.ProviderName("regional"), statuses[0].Name)
	assert.Equal(t, infrav1.DeletingReason, statuses[0].Reason)
	assert.Equal(t, map[string]int{cc.Name + "-regional": 0}, owners)
	bindings.Providers["regional"].Retired = true
	cc.Status.Providers = statuses
	statuses, owners, err = projectProviderStatuses(cc, nil, bindings, false)
	require.NoError(t, err)
	assert.Empty(t, statuses)
	assert.Empty(t, owners)
}

func TestProviderStatusStaleReadinessIsUnknownAndStable(t *testing.T) {
	cc, bindings := attachmentBindingsFixture()
	p := infrav1.CompositeProvider{ObjectMeta: metav1.ObjectMeta{Name: "regional", UID: "attachment-uid", Generation: 2}}
	setTrue(&p.Status.Conditions, 1, infrav1.ReadyCondition)
	statuses, _, err := projectProviderStatuses(cc, []infrav1.CompositeProvider{p}, bindings, false)
	require.NoError(t, err)
	ready := apimeta.FindStatusCondition(statuses[0].Conditions, infrav1.ReadyCondition)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionUnknown, ready.Status)
	assert.Equal(t, p.Generation, ready.ObservedGeneration)
	cc.Status.Providers = statuses
	observed, _, err := projectProviderStatuses(cc, []infrav1.CompositeProvider{p}, bindings, false)
	require.NoError(t, err)
	assert.Equal(t, statuses, observed)
	assert.True(t, isTrue(p.Status.Conditions, infrav1.ReadyCondition), "projection must not edit provider status")
}

func TestProviderStatusRecoveryDoesNotRestoreOldTransitionTime(t *testing.T) {
	cc, bindings := attachmentBindingsFixture()
	p := infrav1.CompositeProvider{ObjectMeta: metav1.ObjectMeta{Name: "regional", UID: "attachment-uid", Generation: 1}}
	setTrue(&p.Status.Conditions, 1, infrav1.ReadyCondition)
	p.Status.Conditions[0].LastTransitionTime = metav1.NewTime(time.Unix(10, 0))
	previous := infrav1.ProviderStatus{Name: "regional"}
	setCondition(&previous.Conditions, 1, infrav1.ReadyCondition, metav1.ConditionUnknown, infrav1.WaitingForProvidersReason, "")
	previous.Conditions[0].LastTransitionTime = metav1.NewTime(time.Unix(20, 0))
	cc.Status.Providers = []infrav1.ProviderStatus{previous}
	statuses, _, err := projectProviderStatuses(cc, []infrav1.CompositeProvider{p}, bindings, false)
	require.NoError(t, err)
	ready := apimeta.FindStatusCondition(statuses[0].Conditions, infrav1.ReadyCondition)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.True(t, ready.LastTransitionTime.After(previous.Conditions[0].LastTransitionTime.Time))
	cc.Status.Providers = statuses
	observed, _, err := projectProviderStatuses(cc, []infrav1.CompositeProvider{p}, bindings, false)
	require.NoError(t, err)
	assert.Equal(t, statuses, observed, "a recovered summary must settle without timestamp churn")
	assert.Equal(t, metav1.NewTime(time.Unix(10, 0)), p.Status.Conditions[0].LastTransitionTime)
}

func TestNodePoolBootstrapRequiresExactClusterAndTemplateKind(t *testing.T) {
	for name, ref := range map[string]map[string]any{
		"current":     {"apiGroup": kubeadmConfigTemplateGVK.Group, "kind": kubeadmConfigTemplateGVK.Kind, "name": "generated"},
		"v1beta1":     {"apiVersion": kubeadmConfigTemplateGVK.Group + "/v1beta1", "kind": kubeadmConfigTemplateGVK.Kind, "name": "generated"},
		"wrong-group": {"apiGroup": "other.test", "kind": kubeadmConfigTemplateGVK.Kind, "name": "generated"},
		"wrong-kind":  {"apiGroup": kubeadmConfigTemplateGVK.Group, "kind": "ForeignTemplate", "name": "generated"},
		"absent":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			pool := observationPool("pool", "owner", ref)
			bootstrap, err := nodePoolBootstrap(pool, "owner")
			require.NoError(t, err)
			if name == "current" || name == "v1beta1" {
				assert.Equal(t, "generated", bootstrap)
			} else {
				assert.Empty(t, bootstrap)
			}
			bootstrap, err = nodePoolBootstrap(pool, "another-owner")
			require.NoError(t, err)
			assert.Empty(t, bootstrap)
		})
	}
}

func observationPool(name, cluster string, ref map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": clusterv1.GroupVersion.String(),
		"kind":       "MachineDeployment",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"clusterName": cluster,
			"template":    map[string]any{"spec": map[string]any{"bootstrap": map[string]any{"configRef": ref}}},
		},
	}}
}

type paginatedPoolReader struct {
	client.Reader
	namespace string
	bootstrap string
	failure   error
	pages     int
}

func (r *paginatedPoolReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if list.GetObjectKind().GroupVersionKind().Kind != "MachineDeploymentList" {
		if err := r.Reader.List(ctx, list, opts...); err != nil {
			return fmt.Errorf("listing test resources: %w", err)
		}
		return nil
	}
	options := (&client.ListOptions{}).ApplyOptions(opts)
	if options.Namespace != r.namespace || options.Limit != 100 {
		return errors.New("node-pool inventory must use a namespaced paginated direct read")
	}
	r.pages++
	if options.Continue != "" && r.failure != nil {
		return r.failure
	}
	poolName := "z-last"
	list.SetContinue("next")
	if options.Continue != "" {
		poolName = "a-first"
		list.SetContinue("")
	}
	pool := observationPool(poolName, "owner", map[string]any{
		"apiGroup": kubeadmConfigTemplateGVK.Group, "kind": kubeadmConfigTemplateGVK.Kind, "name": r.bootstrap,
	})
	nativeList, ok := list.(*unstructured.UnstructuredList)
	if !ok {
		return errors.New("node-pool list must be unstructured")
	}
	nativeList.Items = []unstructured.Unstructured{*pool}
	return nil
}

func TestNodePoolStatusCapacityIsEnforcedBeforePublishing(t *testing.T) {
	for _, existing := range []int{maxProviderNodePools - 2, maxProviderNodePools - 1} {
		t.Run(fmt.Sprintf("existing=%d", existing), func(t *testing.T) {
			mapper := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{clusterv1.GroupVersion})
			mapper.Add(clusterv1.GroupVersion.WithKind("MachineDeployment"), apimeta.RESTScopeNamespace)
			c := fake.NewClientBuilder().WithRESTMapper(mapper).Build()
			reader := &paginatedPoolReader{Reader: c, namespace: "workers", bootstrap: "generated"}
			r := &CompositeClusterReconciler{Client: c, APIReader: reader}
			statuses := []infrav1.ProviderStatus{{Name: "regional", NodePools: make([]infrav1.ChildReference, existing)}}
			err := r.observeNodePools(t.Context(), "workers", "owner", statuses, map[string]int{"generated": 0})
			if existing == maxProviderNodePools-2 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errNodePoolStatusCapacity)
				assert.Contains(t, diagnosticMessage(err), "status reference limit")
			}
			assert.Len(t, statuses[0].NodePools, maxProviderNodePools)
		})
	}
}

func TestProviderStatusDirectPaginationIsAtomicOnReadFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%t", fail), func(t *testing.T) {
			cc, bindings := attachmentBindingsFixture()
			raw, err := encodeAttachmentBindings(cc, bindings)
			require.NoError(t, err)
			cc.Annotations = map[string]string{infrav1.ProviderAttachmentsAnnotation: raw}
			cc.Status.Providers = []infrav1.ProviderStatus{{Name: "regional", NodePools: []infrav1.ChildReference{
				{APIVersion: clusterv1.GroupVersion.String(), Kind: "MachineDeployment", Name: "last-observed"},
			}}}
			cc.Status.ProviderNames = "regional"
			before := cc.DeepCopy()
			p := &infrav1.CompositeProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "regional", Namespace: cc.Namespace, UID: "attachment-uid", Generation: 1},
			}
			p.Spec.ClusterRef.Name = cc.Name
			setTrue(&p.Status.Conditions, 1, infrav1.ReadyCondition)
			scheme := runtime.NewScheme()
			require.NoError(t, infrav1.AddToScheme(scheme))
			mapper := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{clusterv1.GroupVersion})
			mapper.Add(clusterv1.GroupVersion.WithKind("MachineDeployment"), apimeta.RESTScopeNamespace)
			c := fake.NewClientBuilder().WithScheme(scheme).WithRESTMapper(mapper).WithObjects(p).Build()
			reader := &paginatedPoolReader{Reader: c, namespace: cc.Namespace, bootstrap: cc.Name + "-regional"}
			failure := errors.New("node-pool list failure SECRET-CANARY")
			if fail {
				reader.failure = failure
			}
			r := &CompositeClusterReconciler{Client: c, APIReader: reader}
			err = r.observeProviderStatuses(t.Context(), cc, &clusterFacts{Name: "owner"})
			assert.Equal(t, 2, reader.pages)
			if fail {
				require.ErrorIs(t, err, failure)
				assert.Equal(t, before.Status, cc.Status, "a partial list must not replace the last complete inventory")
				return
			}
			require.NoError(t, err)
			require.Len(t, cc.Status.Providers, 1)
			assert.Equal(t, []infrav1.ChildReference{
				{APIVersion: clusterv1.GroupVersion.String(), Kind: "MachineDeployment", Name: "a-first"},
				{APIVersion: clusterv1.GroupVersion.String(), Kind: "MachineDeployment", Name: "z-last"},
			}, cc.Status.Providers[0].NodePools)
		})
	}
}
