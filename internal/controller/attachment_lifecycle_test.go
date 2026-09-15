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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
)

func TestEmptyClusterReadyBeforeAttachmentsOrEndpoint(t *testing.T) {
	cc, _ := attachmentBindingsFixture()
	r := &CompositeClusterReconciler{}
	result, err := r.reconcileNormal(t.Context(), cc, &clusterFacts{}, &compositionPlan{})
	require.NoError(t, err)
	assert.Equal(t, steady, result)
	assert.True(t, isTrue(cc.Status.Conditions, infrav1.ReadyCondition))
	require.NotNil(t, cc.Status.Initialization.Provisioned)
	assert.True(t, *cc.Status.Initialization.Provisioned)
	assert.Empty(t, cc.Status.Providers)
	assert.Empty(t, cc.Status.ProviderNames)
	assert.Empty(t, cc.Status.FailureDomains)
}

func TestCanonicalAttachmentRetirementProtection(t *testing.T) {
	cc, bindings := attachmentBindingsFixture()
	regional := *bindings.Providers["regional"]
	regional.Binding.ChildName = "consumer-other"
	bindings.Providers["other"] = &regional
	assert.True(t, canonicalAttachmentRequired(cc, "regional", bindings))
	assert.False(t, canonicalAttachmentRequired(cc, "other", bindings))
	regional.Retired = true
	assert.False(t, canonicalAttachmentRequired(cc, "regional", bindings))
}

type failingChildReader struct {
	client.Reader
	err error
}

func (r failingChildReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return r.err
}

func TestChildTeardownUsesDirectReadAndConfirmsAbsence(t *testing.T) {
	cc, _ := attachmentBindingsFixture()
	scheme := runtime.NewScheme()
	require.NoError(t, infrav1.AddToScheme(scheme))
	gvk := schema.GroupVersionKind{Group: "infra.test", Version: "v1beta1", Kind: "FakeCluster"}
	child := &unstructured.Unstructured{}
	child.SetGroupVersionKind(gvk)
	child.SetName(cc.Name)
	child.SetNamespace(cc.Namespace)
	child.SetUID("child-uid")
	child.SetOwnerReferences(
		[]metav1.OwnerReference{
			{APIVersion: infrav1.GroupVersion.String(), Kind: compositeClusterKind, Name: cc.Name, UID: cc.UID, Controller: new(true)},
		},
	)
	direct := fake.NewClientBuilder().WithScheme(scheme).WithObjects(child).Build()
	// The cache has no child. Only a successful direct observation proves absence.
	cached := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &CompositeClusterReconciler{Client: direct, APIReader: direct}
	unavailable := errors.New("native API unavailable")
	r.APIReader = failingChildReader{Reader: cached, err: unavailable}
	gone, err := r.childGone(t.Context(), cc, gvk, cc.Name)
	assert.False(t, gone)
	require.ErrorIs(t, err, unavailable)
	r.APIReader = direct
	gone, err = r.childGone(t.Context(), cc, gvk, cc.Name)
	require.NoError(t, err)
	assert.False(t, gone, "a successful delete is not a confirmed absence")
	gone, err = r.childGone(t.Context(), cc, gvk, cc.Name)
	require.NoError(t, err)
	assert.True(t, gone)
}

func TestChildTeardownRejectsForeignOwnership(t *testing.T) {
	cc, _ := attachmentBindingsFixture()
	scheme := runtime.NewScheme()
	require.NoError(t, infrav1.AddToScheme(scheme))
	child := &unstructured.Unstructured{}
	gvk := schema.GroupVersionKind{Group: "infra.test", Version: "v1beta1", Kind: "FakeCluster"}
	child.SetGroupVersionKind(gvk)
	child.SetName(cc.Name)
	child.SetNamespace(cc.Namespace)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(child).Build()
	r := &CompositeClusterReconciler{Client: c, APIReader: c}
	gone, err := r.childGone(t.Context(), cc, gvk, cc.Name)
	assert.False(t, gone)
	require.ErrorIs(t, err, capi.ErrChildNotOwned)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(child), child))
}
