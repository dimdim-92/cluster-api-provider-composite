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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func catalogProfile() *infrav1.CompositeProviderTemplate {
	return &infrav1.CompositeProviderTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "profile-a", UID: "profile-a-uid", Generation: 1},
		Spec: infrav1.CompositeProviderTemplateSpec{
			Provider: "fake", Account: "account-a", Region: "region-a",
			InfraClusterKind: infrav1.NativeInfraClusterKind{APIGroup: infrav1.GroupVersion.Group, Kind: "FakeCluster"},
			InfrastructureTemplateRef: infrav1.ProviderTemplateReference{
				APIVersion: infrav1.GroupVersion.Group + "/v1beta1", Kind: "FakeClusterTemplate", Namespace: "catalog", Name: "infrastructure-r1",
			},
			BootstrapTemplateRef: infrav1.ProviderTemplateReference{
				APIVersion: "bootstrap.cluster.x-k8s.io/v1beta2", Kind: "KubeadmConfigTemplate", Namespace: "catalog", Name: "bootstrap-r1",
			},
		},
	}
}

func catalogClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, infrav1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&infrav1.CompositeProviderTemplate{}).WithObjects(objects...).Build()
}

func TestCompositeProvider_ProtectsAndValidates(t *testing.T) {
	p := catalogProfile()
	c := catalogClient(t, p,
		catalogSourceTemplate(p.Spec.InfrastructureTemplateRef, map[string]any{}),
		catalogSourceTemplate(p.Spec.BootstrapTemplateRef, map[string]any{}))
	r := &CompositeProviderTemplateReconciler{Client: c, APIReader: c}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)}
	_, err := r.Reconcile(t.Context(), request)
	require.NoError(t, err)
	require.NoError(t, c.Get(t.Context(), request.NamespacedName, p))
	assert.Contains(t, p.Finalizers, infrav1.ProviderInUseFinalizer)
	require.Len(t, p.Status.Conditions, 1)
	assert.Equal(t, infrav1.ConfigurationValidCondition, p.Status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, p.Status.Conditions[0].Status)
	before := p.DeepCopy()
	_, err = r.Reconcile(t.Context(), request)
	require.NoError(t, err)
	require.NoError(t, c.Get(t.Context(), request.NamespacedName, p))
	assert.Equal(t, before, p, "unchanged profile observations must not write")
}

func TestCompositeProvider_HoldsAttachmentReferences(t *testing.T) {
	p := catalogProfile()
	p.Finalizers = []string{infrav1.ProviderInUseFinalizer}
	consumer := &infrav1.CompositeProvider{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "regional",
			Namespace: "outside-watch-scope",
			Labels:    map[string]string{"cluster.x-k8s.io/watch-filter": "other"},
		},
		Spec: infrav1.CompositeProviderSpec{TemplateRef: infrav1.CompositeProviderReference{Name: p.Name}},
	}
	c := catalogClient(t, p, consumer)
	require.NoError(t, c.Delete(t.Context(), p))
	r := &CompositeProviderTemplateReconciler{Client: c, APIReader: c}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)}
	result, err := r.Reconcile(t.Context(), request)
	require.NoError(t, err)
	assert.Positive(t, result.RequeueAfter)
	require.NoError(t, c.Get(t.Context(), request.NamespacedName, p))
	assert.Contains(t, p.Finalizers, infrav1.ProviderInUseFinalizer)
	require.NoError(t, c.Delete(t.Context(), consumer))
	_, err = r.Reconcile(t.Context(), request)
	require.NoError(t, err)
	assert.True(t, apierrors.IsNotFound(c.Get(t.Context(), request.NamespacedName, p)))
}

type catalogReader struct {
	client.Reader
	list func(context.Context, client.ObjectList, ...client.ListOption) error
}

func (r catalogReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return r.list(ctx, list, opts...)
}

func TestCompositeProvider_ReferenceReadFailureHoldsFinalizer(t *testing.T) {
	p := catalogProfile()
	p.Finalizers = []string{infrav1.ProviderInUseFinalizer}
	c := catalogClient(t, p)
	require.NoError(t, c.Delete(t.Context(), p))
	reader := catalogReader{Reader: c, list: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*infrav1.CompositeProviderList); ok {
			return apierrors.NewForbidden(
				schema.GroupResource{Group: infrav1.GroupVersion.Group, Resource: "compositeproviders"},
				"",
				errors.New("SECRET-CANARY"),
			)
		}
		return c.List(ctx, list, opts...)
	}}
	r := &CompositeProviderTemplateReconciler{Client: c, APIReader: reader}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	require.True(t, apierrors.IsForbidden(err), "%v", err)
	assert.NotContains(t, err.Error(), "SECRET-CANARY")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(p), p))
	assert.Contains(t, p.Finalizers, infrav1.ProviderInUseFinalizer)
}

func TestCompositeProvider_ReferencePaginationUsesGlobalReader(t *testing.T) {
	p := catalogProfile()
	p.Finalizers = []string{infrav1.ProviderInUseFinalizer}
	c := catalogClient(t, p)
	require.NoError(t, c.Delete(t.Context(), p))
	calls := 0
	reader := catalogReader{Reader: c, list: func(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
		options := (&client.ListOptions{}).ApplyOptions(opts)
		assert.Empty(t, options.Namespace)
		assert.Nil(t, options.LabelSelector)
		assert.Nil(t, options.FieldSelector)
		assert.Positive(t, options.Limit)
		clusters, ok := list.(*infrav1.CompositeProviderList)
		if !ok {
			return nil
		}
		calls++
		if calls == 1 {
			assert.Empty(t, options.Continue)
			clusters.Continue = "next-page"
			return nil
		}
		assert.Equal(t, "next-page", options.Continue)
		clusters.Continue = ""
		clusters.Items = []infrav1.CompositeProvider{
			{Spec: infrav1.CompositeProviderSpec{TemplateRef: infrav1.CompositeProviderReference{Name: p.Name}}},
		}
		return nil
	}}
	r := &CompositeProviderTemplateReconciler{Client: c, APIReader: reader}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(p), p))
	assert.Contains(t, p.Finalizers, infrav1.ProviderInUseFinalizer)
}

func TestCompositeProvider_InvalidSourcePayloadIsSafe(t *testing.T) {
	p := catalogProfile()
	source := catalogSourceTemplate(p.Spec.InfrastructureTemplateRef, map[string]any{})
	source.Object["spec"] = map[string]any{"template": map[string]any{"spec": []any{"SECRET-CANARY"}}}
	c := catalogClient(t, p, source, catalogSourceTemplate(p.Spec.BootstrapTemplateRef, map[string]any{}))
	r := &CompositeProviderTemplateReconciler{Client: c, APIReader: c}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	require.NoError(t, err, "invalid catalog data is reported as a condition")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(p), p))
	require.Len(t, p.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionFalse, p.Status.Conditions[0].Status)
	assert.NotContains(t, p.Status.Conditions[0].Message, "SECRET-CANARY")
	assert.Contains(t, p.Finalizers, infrav1.ProviderInUseFinalizer)
}

func TestCompositeProvider_DurableReferenceRetainsProfile(t *testing.T) {
	p := catalogProfile()
	p.Finalizers = []string{infrav1.ProviderInUseFinalizer}
	cc, bindings := attachmentBindingsFixture()
	raw, err := encodeAttachmentBindings(cc, bindings)
	require.NoError(t, err)
	cc.Annotations = map[string]string{infrav1.ProviderAttachmentsAnnotation: raw}
	c := catalogClient(t, p, cc)
	require.NoError(t, c.Delete(t.Context(), p))
	r := &CompositeProviderTemplateReconciler{Client: c, APIReader: c}
	_, err = r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	require.NoError(t, err)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(p), p))
	assert.Contains(t, p.Finalizers, infrav1.ProviderInUseFinalizer)
}

func TestCompositeProvider_ReferenceCreatedAfterScanCannotBindDeletedProfile(t *testing.T) {
	p := catalogProfile()
	p.Finalizers = []string{infrav1.ProviderInUseFinalizer}
	c := catalogClient(t, p)
	require.NoError(t, c.Delete(t.Context(), p))
	consumer := &infrav1.CompositeProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "late-consumer", Namespace: "outside"},
		Spec:       infrav1.CompositeProviderSpec{TemplateRef: infrav1.CompositeProviderReference{Name: p.Name}},
	}
	reader := catalogReader{Reader: c, list: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
		if err := c.List(ctx, list, opts...); err != nil {
			return fmt.Errorf("listing catalog consumers: %w", err)
		}
		if _, ok := list.(*infrav1.CompositeProviderList); ok {
			// The returned snapshot cannot include a consumer created after it.
			require.NoError(t, c.Create(ctx, consumer))
		}
		return nil
	}}
	r := &CompositeProviderTemplateReconciler{Client: c, APIReader: reader}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(c.Get(t.Context(), client.ObjectKeyFromObject(p), p)))
	clusterController := &CompositeClusterReconciler{Client: c, APIReader: c}
	_, err = clusterController.readUsableProfile(t.Context(), consumer.Spec.TemplateRef.Name, false)
	require.ErrorIs(t, err, errProfileUnavailable, "a late unresolved reference must not authorize provisioning")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(consumer), consumer))
	assert.Empty(t, consumer.Annotations[infrav1.ProviderBindingAnnotation])
	assert.Empty(t, consumer.Finalizers)
}
