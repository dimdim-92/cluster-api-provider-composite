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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

type nodePoolFailureReader struct {
	client.Reader
	cause error
}

func (r nodePoolFailureReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, native := list.(*unstructured.UnstructuredList); native && list.GetObjectKind().GroupVersionKind().Kind == "MachineDeploymentList" {
		return r.cause
	}
	if err := r.Reader.List(ctx, list, opts...); err != nil {
		return fmt.Errorf("observing test resources: %w", err)
	}
	return nil
}

func TestProviderStatus_InventoryReadFailurePreservesSummary(t *testing.T) {
	template := newRegionalProfile(t)
	cluster := newCAPICluster(t, newNamespace(t), "inventory-failure")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{
		ControlPlaneEndpoint: infrav1.APIEndpoint{Host: "192.0.2.81", Port: 6443},
	})
	p := addonProvider(t, cc, template.Name, "paris")
	addonWorkerPool(t, cc.Namespace, cluster.Name, "workers", cc.Name+"-paris")
	r := newClusterReconciler()
	_, cc, err := reconcileCluster(t, r, cc)
	require.NoError(t, err)
	markFakeClusterReady(t, cc.Namespace, cc.Name)
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	require.Len(t, cc.Status.Providers, 1)
	before := cc.DeepCopy()
	cause := errors.New("inventory read failure SECRET-CANARY")
	r.APIReader = nodePoolFailureReader{Reader: k8s, cause: cause}
	_, cc, err = reconcileCluster(t, r, cc)
	require.ErrorIs(t, err, cause)
	assert.NotContains(t, err.Error(), "SECRET-CANARY")
	require.Len(t, cc.Status.Providers, 1)
	current := &cc.Status.Providers[0]
	assert.Equal(t, before.Status.Providers[0].ChildRef, current.ChildRef)
	assert.Equal(t, before.Status.Providers[0].BootstrapTemplateRef, current.BootstrapTemplateRef)
	assert.Equal(t, before.Status.Providers[0].NodePools, current.NodePools)
	assert.Equal(t, before.Status.Providers[0].Provisioned, current.Provisioned)
	requireCondition(t, current.Conditions, infrav1.ReadyCondition, metav1.ConditionUnknown, infrav1.WaitingForProvidersReason)
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	requireCondition(t, getAddon(t, p).Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	status, marshalErr := json.Marshal(cc.Status)
	require.NoError(t, marshalErr)
	assert.NotContains(t, string(status), "SECRET-CANARY")

	r.APIReader = k8s
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	requireCondition(t, cc.Status.Providers[0].Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	assertProviderPoolNames(t, &cc.Status.Providers[0], "workers")
}

type attachmentFailureReader struct {
	client.Reader
	target client.ObjectKey
	cause  error
}

func (r attachmentFailureReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key == r.target {
		return r.cause
	}
	if err := r.Reader.Get(ctx, key, obj, opts...); err != nil {
		return fmt.Errorf("observing test object: %w", err)
	}
	return nil
}

func TestProviderAddon_SourceReadFailurePreservesObservations(t *testing.T) {
	template := newRegionalProfile(t)
	cluster := newCAPICluster(t, newNamespace(t), "observation-addon")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	p := addonProvider(t, cc, template.Name, "regional")
	r := newClusterReconciler()
	setEndpoint(t, cc, cluster, "203.0.113.10", 7443)
	_, cc, err := reconcileCluster(t, r, cc)
	require.NoError(t, err)
	markFakeClusterReady(t, cc.Namespace, cc.Name)
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	before := getAddon(t, p)
	requireCondition(t, before.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	domains := cc.Status.FailureDomains
	pins := cc.Annotations[infrav1.ProviderAttachmentsAnnotation]
	cause := errors.New("native source transport failure SECRET-CANARY")
	r.APIReader = attachmentFailureReader{
		Reader: k8s,
		target: client.ObjectKey{Namespace: template.Spec.InfrastructureTemplateRef.Namespace, Name: template.Spec.InfrastructureTemplateRef.Name},
		cause:  cause,
	}
	_, current, err := reconcileCluster(t, r, cc)
	require.ErrorIs(t, err, cause)
	assert.NotContains(t, err.Error(), "SECRET-CANARY")
	fresh := getAddon(t, p)
	requireCondition(t, fresh.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionUnknown, infrav1.WaitingForProvidersReason)
	assert.Equal(t, before.Status.Initialization, fresh.Status.Initialization)
	assert.Equal(t, before.Status.InfrastructureRef, fresh.Status.InfrastructureRef)
	assert.Equal(t, before.Status.BootstrapTemplateRef, fresh.Status.BootstrapTemplateRef)
	assert.Equal(t, pins, current.Annotations[infrav1.ProviderAttachmentsAnnotation])
	assert.Equal(t, domains, current.Status.FailureDomains)
	status, marshalErr := json.Marshal(fresh.Status)
	require.NoError(t, marshalErr)
	assert.NotContains(t, string(status), "SECRET-CANARY")

	r.APIReader = k8s
	_, _, err = reconcileCluster(t, r, current)
	require.NoError(t, err)
	requireCondition(t, getAddon(t, p).Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
}
