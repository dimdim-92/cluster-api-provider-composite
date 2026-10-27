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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/profile"
)

func catalogSourceTemplate(ref infrav1.ProviderTemplateReference, payload map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"template": map[string]any{"spec": payload}}}}
	obj.SetAPIVersion(ref.APIVersion)
	obj.SetKind(ref.Kind)
	obj.SetNamespace(ref.Namespace)
	obj.SetName(ref.Name)
	obj.SetUID(types.UID(ref.Name + "-uid"))
	obj.SetGeneration(1)
	return obj
}

type failingSourceReader struct{ client.Reader }

func (r failingSourceReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, source := obj.(*unstructured.Unstructured); source {
		return apierrors.NewTimeoutError("SECRET-CANARY", 1)
	}
	if err := r.Reader.Get(ctx, key, obj, opts...); err != nil {
		return fmt.Errorf("reading catalog object: %w", err)
	}
	return nil
}

func TestProfileTemplates_TransientReadIsUnknown(t *testing.T) {
	p := catalogProfile()
	c := catalogClient(t, p)
	r := &CompositeProviderTemplateReconciler{Client: c, APIReader: failingSourceReader{Reader: c}}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	require.True(t, apierrors.IsTimeout(err), "%v", err)
	assert.NotContains(t, err.Error(), "SECRET-CANARY")
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(p), p))
	require.Len(t, p.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionUnknown, p.Status.Conditions[0].Status)
	assert.NotContains(t, p.Status.Conditions[0].Message, "SECRET-CANARY")
}

func TestResolveProfileTemplates_ReadsIndependentPayloads(t *testing.T) {
	p := catalogProfile()
	infrastructure := catalogSourceTemplate(p.Spec.InfrastructureTemplateRef, map[string]any{"big": int64(9007199254740993)})
	bootstrap := catalogSourceTemplate(p.Spec.BootstrapTemplateRef, map[string]any{"format": "cloud-config"})
	c := catalogClient(t, p, infrastructure, bootstrap)
	loaded, err := resolveProfileTemplates(t.Context(), c, p)
	require.NoError(t, err)
	assert.Equal(t, sourceRevision{UID: infrastructure.GetUID(), Generation: 1}, loaded.infrastructure)
	assert.Equal(t, sourceRevision{UID: bootstrap.GetUID(), Generation: 1}, loaded.bootstrap)
	resolved, err := profile.Resolve(&loaded.configuration, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, `{"big":9007199254740993}`, string(loaded.configuration.Defaults.InfraSpec.Raw))
	resolved.InfraSpec["big"], resolved.Bootstrap["format"] = "changed", "changed"
	fresh := infrastructure.DeepCopy()
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(infrastructure), fresh))
	value, found, err := unstructured.NestedInt64(fresh.Object, "spec", "template", "spec", "big")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(9007199254740993), value)
}

func TestResolveProfileTemplates_RejectsInvalidSources(t *testing.T) {
	for _, scenario := range []string{"missing", "namespace", "kind", "bootstrap-version", "payload", "deleting"} {
		t.Run(scenario, func(t *testing.T) {
			p := catalogProfile()
			infrastructure := catalogSourceTemplate(p.Spec.InfrastructureTemplateRef, map[string]any{})
			bootstrap := catalogSourceTemplate(p.Spec.BootstrapTemplateRef, map[string]any{})
			want := errInvalidTemplate
			switch scenario {
			case "missing":
				p.Spec.InfrastructureTemplateRef.Name, want = "absent", errTemplateUnavailable
			case "namespace":
				p.Spec.InfrastructureTemplateRef.Namespace = ""
			case "kind":
				p.Spec.InfrastructureTemplateRef.Kind = "CompositeClusterTemplate"
			case "bootstrap-version":
				p.Spec.BootstrapTemplateRef.APIVersion = "bootstrap.cluster.x-k8s.io/v1beta1"
			case "payload":
				infrastructure.Object["spec"] = map[string]any{"template": map[string]any{"spec": []any{"SECRET-CANARY"}}}
			case "deleting":
				infrastructure.SetFinalizers([]string{"test.example.org/hold"})
				now := metav1.Now()
				infrastructure.SetDeletionTimestamp(&now)
				want = errTemplateUnavailable
			}
			_, err := resolveProfileTemplates(t.Context(), catalogClient(t, infrastructure, bootstrap), p)
			require.ErrorIs(t, err, want)
			assert.NotContains(t, err.Error(), "SECRET-CANARY")
		})
	}
}

func TestProfileBinding_UsesSourceIdentityWithoutContentHash(t *testing.T) {
	p := catalogProfile()
	cc := &infrav1.CompositeCluster{ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: "workload"}}
	entry := &infrav1.ProviderSpec{
		Name:             "regional",
		ProviderRef:      infrav1.CompositeProviderReference{Name: p.Name},
		InfraClusterName: "consumer-region-b",
	}
	sources := &profileTemplates{
		infrastructure: sourceRevision{UID: "infra-uid", Generation: 1},
		bootstrap:      sourceRevision{UID: "bootstrap-uid", Generation: 1},
	}
	initial, err := profileBindingFor(cc, entry, p, sources, nil)
	require.NoError(t, err)
	assert.Equal(t, "consumer-region-b", initial.ChildName)
	for _, scenario := range []string{"metadata", "profile-recreated", "infra-edited", "bootstrap-recreated"} {
		t.Run(scenario, func(t *testing.T) {
			catalog, templates := p.DeepCopy(), *sources
			switch scenario {
			case "metadata":
				catalog.Labels = map[string]string{"test.example.org/changed": "true"}
			case "profile-recreated":
				catalog.UID = "another-uid"
			case "infra-edited":
				templates.infrastructure.Generation++
			case "bootstrap-recreated":
				templates.bootstrap.UID = "another-uid"
			}
			_, err := profileBindingFor(cc, entry, catalog, &templates, &initial)
			if scenario == "metadata" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errProfileMismatch)
			}
		})
	}
}

func TestProfileBinding_RejectsSourceOutputCollision(t *testing.T) {
	p := catalogProfile()
	cc := &infrav1.CompositeCluster{ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: "workload"}}
	entry := &infrav1.ProviderSpec{Name: "regional", ProviderRef: infrav1.CompositeProviderReference{Name: p.Name}}
	p.Spec.BootstrapTemplateRef.Namespace, p.Spec.BootstrapTemplateRef.Name = cc.Namespace, "consumer-regional"
	_, err := profileBindingFor(cc, entry, p, &profileTemplates{}, nil)
	require.ErrorIs(t, err, errInvalidTemplate)
}
