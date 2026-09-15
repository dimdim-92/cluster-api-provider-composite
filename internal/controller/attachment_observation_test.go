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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
	"github.com/numspot/cluster-api-provider-composite/internal/profile"
)

// Child operations are emulated, while attachment status is really persisted by
// the fake client. Successful native creates return the provider's ready response.
type attachmentObservationClient struct {
	client.Client
	operation    string
	failure      error
	patchFailure error
}

func (c *attachmentObservationClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	_, crd := obj.(*apiextensionsv1.CustomResourceDefinition)
	if c.operation == "contract" && crd || c.operation == "read" && obj.GetObjectKind().GroupVersionKind().Kind == "FakeCluster" {
		return c.failure
	}
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return fmt.Errorf("observing test object: %w", err)
	}
	return nil
}

func (c *attachmentObservationClient) Apply(_ context.Context, _ runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
	if c.operation == "apply" {
		return c.failure
	}
	return nil
}

func (c *attachmentObservationClient) Create(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
	if obj.GetObjectKind().GroupVersionKind().Kind == "KubeadmConfigTemplate" {
		if c.operation == "bootstrap" {
			return c.failure
		}
		return nil
	}
	if c.operation == "create" {
		return c.failure
	}
	if native, ok := obj.(*unstructured.Unstructured); ok {
		native.Object["status"] = map[string]any{"ready": true}
	}
	return nil
}

func (c *attachmentObservationClient) Status() client.SubResourceWriter {
	return attachmentObservationStatus{SubResourceWriter: c.Client.Status(), failure: c.patchFailure}
}

type attachmentObservationStatus struct {
	client.SubResourceWriter
	failure error
}

func (s attachmentObservationStatus) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if s.failure != nil {
		return s.failure
	}
	if err := s.SubResourceWriter.Patch(ctx, obj, patch, opts...); err != nil {
		return fmt.Errorf("patching test status: %w", err)
	}
	return nil
}

func attachmentObservationFixture(
	t *testing.T,
) (*CompositeClusterReconciler, *attachmentObservationClient, *infrav1.CompositeCluster, *plannedAttachment) {
	t.Helper()
	cc, bindings := attachmentBindingsFixture()
	binding := bindings.Providers["regional"].Binding
	p := &infrav1.CompositeProvider{ObjectMeta: metav1.ObjectMeta{Name: "regional", Namespace: cc.Namespace, UID: "attachment-uid", Generation: 2}}
	p.Status.Initialization.Provisioned = new(true)
	p.Status.Profile = binding.profileStatus()
	p.Status.InfrastructureRef = infrav1.ChildReference{APIVersion: "infra.test/v1beta1", Kind: binding.Kind, Name: binding.ChildName}
	p.Status.BootstrapTemplateRef = infrav1.ChildReference{
		APIVersion: kubeadmConfigTemplateGVK.GroupVersion().String(),
		Kind:       kubeadmConfigTemplateGVK.Kind,
		Name:       binding.BootstrapName,
	}
	p.Status.ObservedGeneration = 1
	for _, condition := range []string{infrav1.ReadyCondition, infrav1.EndpointPropagatedCondition, infrav1.BootstrapTemplatesReadyCondition} {
		setTrue(&p.Status.Conditions, 1, condition)
	}
	scheme := runtime.NewScheme()
	require.NoError(t, infrav1.AddToScheme(scheme))
	require.NoError(t, clusterv1.AddToScheme(scheme))
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "infra.test", Version: "v1beta1", Kind: "FakeCluster"}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(kubeadmConfigTemplateGVK, &unstructured.Unstructured{})
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "fakeclusters.infra.test", Labels: map[string]string{"cluster.x-k8s.io/v1beta1": "v1beta1"}},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "infra.test", Scope: apiextensionsv1.NamespaceScoped,
			Names:    apiextensionsv1.CustomResourceDefinitionNames{Kind: "FakeCluster", Plural: "fakeclusters"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1beta1", Served: true, Storage: true}},
		},
	}
	c := &attachmentObservationClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(p).WithObjects(p, crd).Build()}
	r := &CompositeClusterReconciler{
		Client:    c,
		Contracts: capi.NewContractCache(c),
		Clusters:  NewClusterReader(c, clusterv1.GroupVersion.WithKind("Cluster")),
	}
	entry := &plannedAttachment{provider: p, plan: providerPlan{
		spec: &infrav1.ProviderSpec{Name: "regional"}, binding: binding,
		payload: &profile.Resolved{InfraSpec: map[string]any{}, Bootstrap: map[string]any{}},
	}}
	return r, c, cc, entry
}

func TestAttachmentOperationFailureInvalidatesReadyAndRecovers(t *testing.T) {
	for _, operation := range []string{"contract", "read", "apply", "create", "bootstrap"} {
		t.Run(operation, func(t *testing.T) {
			r, c, cc, entry := attachmentObservationFixture(t)
			before := entry.provider.DeepCopy()
			secret := "RAW-SECRET-" + strings.Repeat("sensitive", 1024)
			failure := errors.New(secret)
			c.operation, c.failure = operation, failure
			endpoint := infrav1.APIEndpoint{Host: "api.example.test", Port: 6443}
			cluster := &clusterFacts{Name: "owner", Namespace: cc.Namespace, UID: "owner-uid"}

			ready, err := r.reconcileAttachment(t.Context(), cc, cluster, endpoint, entry)
			assert.False(t, ready)
			require.ErrorIs(t, err, failure)
			stored := &infrav1.CompositeProvider{}
			require.NoError(t, c.Client.Get(t.Context(), client.ObjectKeyFromObject(entry.provider), stored))
			condition := apimeta.FindStatusCondition(stored.Status.Conditions, infrav1.ReadyCondition)
			require.NotNil(t, condition)
			assert.Equal(t, metav1.ConditionUnknown, condition.Status)
			assert.Equal(t, infrav1.WaitingForProvidersReason, condition.Reason)
			assert.Equal(t, stored.Generation, condition.ObservedGeneration)
			assert.NotEmpty(t, condition.Message)
			assert.LessOrEqual(t, len(condition.Message), 1024)
			assert.NotContains(t, condition.Message, "RAW-SECRET")
			assert.NotContains(t, condition.Message, "sensitive")
			assert.Equal(t, stored.Generation, stored.Status.ObservedGeneration)
			assert.Equal(t, before.Status.Initialization, stored.Status.Initialization)
			assert.Equal(t, before.Status.Profile, stored.Status.Profile)
			assert.Equal(t, before.Status.InfrastructureRef, stored.Status.InfrastructureRef)
			assert.Equal(t, before.Status.BootstrapTemplateRef, stored.Status.BootstrapTemplateRef)

			c.operation = ""
			entry.provider = stored
			ready, err = r.reconcileAttachment(t.Context(), cc, cluster, endpoint, entry)
			require.NoError(t, err)
			assert.True(t, ready)
			require.NoError(t, c.Client.Get(t.Context(), client.ObjectKeyFromObject(stored), stored))
			assert.True(t, isTrue(stored.Status.Conditions, infrav1.ReadyCondition))
			assert.True(t, *stored.Status.Initialization.Provisioned)
			assert.Equal(t, before.Status.InfrastructureRef, stored.Status.InfrastructureRef)
			assert.Equal(t, before.Status.BootstrapTemplateRef, stored.Status.BootstrapTemplateRef)
		})
	}
}

func TestAttachmentOperationAndStatusPatchErrorsAreJoined(t *testing.T) {
	r, c, cc, entry := attachmentObservationFixture(t)
	operationError := errors.New("native API unavailable")
	patchError := errors.New("status update unavailable")
	c.operation, c.failure, c.patchFailure = "contract", operationError, patchError
	ready, err := r.reconcileAttachment(t.Context(), cc, &clusterFacts{}, infrav1.APIEndpoint{}, entry)
	assert.False(t, ready)
	require.ErrorIs(t, err, operationError)
	require.ErrorIs(t, err, patchError)
}
