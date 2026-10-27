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
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/controller"
)

func newRegionalProfile(t *testing.T) *infrav1.CompositeProviderTemplate {
	t.Helper()
	p := regionalProfileDefinition()
	createRegionalProfile(t, k8s, p)
	protectRegionalProfile(t, p)
	return p
}

func regionalProfileDefinition() *infrav1.CompositeProviderTemplate {
	return &infrav1.CompositeProviderTemplate{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "regional-profile-"},
		Spec: infrav1.CompositeProviderTemplateSpec{
			Provider: "fake", Account: "account-a", Region: "region-a", Zones: []string{"zone-a"},
			InfraClusterKind: infrav1.NativeInfraClusterKind{APIGroup: fakeClusterGVK.Group, Kind: fakeClusterGVK.Kind},
			InfrastructureTemplateRef: infrav1.ProviderTemplateReference{
				APIVersion: fakeClusterGVK.GroupVersion().
					String(),
				Kind:      "FakeClusterTemplate",
				Namespace: "default",
				Name:      "regional-infrastructure",
			},
			BootstrapTemplateRef: infrav1.ProviderTemplateReference{
				APIVersion: kubeadmConfigTemplateGVK.GroupVersion().
					String(),
				Kind:      kubeadmConfigTemplateGVK.Kind,
				Namespace: "default",
				Name:      "regional-bootstrap",
			},
			OverridePolicy: &infrav1.CompositeProviderOverridePolicy{
				InfraSpecPaths:          []infrav1.ProfileJSONPointer{"/replicaCount"},
				ProtectedInfraSpecPaths: []infrav1.ProfileJSONPointer{"/identityRef"},
			},
		},
	}
}

func createRegionalProfile(t *testing.T, c client.Client, p *infrav1.CompositeProviderTemplate) {
	t.Helper()
	namespace := operatorNamespace(t, c, "profile-sources")
	p.Spec.InfrastructureTemplateRef = infrav1.ProviderTemplateReference{
		APIVersion: p.Spec.InfraClusterKind.APIGroup + "/v1beta1",
		Kind:       p.Spec.InfraClusterKind.Kind + "Template",
		Namespace:  namespace,
		Name:       "infrastructure",
	}
	p.Spec.BootstrapTemplateRef = infrav1.ProviderTemplateReference{
		APIVersion: kubeadmConfigTemplateGVK.GroupVersion().String(), Kind: kubeadmConfigTemplateGVK.Kind, Namespace: namespace, Name: "bootstrap",
	}
	createProfileSourceTemplates(t, c, p)
	require.NoError(t, c.Create(t.Context(), p))
}

func createProfileSourceTemplates(t *testing.T, c client.Client, p *infrav1.CompositeProviderTemplate) {
	t.Helper()
	payload := map[string]any{"replicaCount": int64(1), "big": int64(9007199254740993), "identityRef": map[string]any{"name": "fixed"}}
	infrastructure := profileSourceObject(p.Spec.InfrastructureTemplateRef, payload)
	bootstrap := profileSourceObject(p.Spec.BootstrapTemplateRef, map[string]any{
		"joinConfiguration": map[string]any{"nodeRegistration": map[string]any{"kubeletExtraArgs": []any{
			map[string]any{"name": "provider-id", "value": "fake:///native"},
		}}},
	})
	require.NoError(t, c.Create(t.Context(), infrastructure))
	require.NoError(t, c.Create(t.Context(), bootstrap))
}

func profileSourceObject(ref infrav1.ProviderTemplateReference, spec map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"template": map[string]any{"spec": spec}}}}
	obj.SetGroupVersionKind(schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind))
	obj.SetNamespace(ref.Namespace)
	obj.SetName(ref.Name)
	return obj
}

func protectRegionalProfile(t *testing.T, p *infrav1.CompositeProviderTemplate) {
	t.Helper()
	r := &controller.CompositeProviderTemplateReconciler{Client: k8s, APIReader: k8s}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	require.NoError(t, err)
	require.NoError(t, k8s.Get(t.Context(), client.ObjectKeyFromObject(p), p))
	require.Contains(t, p.Finalizers, infrav1.ProviderInUseFinalizer)
}

func deleteProfileConsumer(t *testing.T, r *controller.CompositeClusterReconciler, cc *infrav1.CompositeCluster) {
	t.Helper()
	require.NoError(t, k8s.Delete(t.Context(), cc))
	key := client.ObjectKeyFromObject(cc)
	require.Eventually(t, func() bool {
		_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
		if !assert.NoError(t, err) {
			return false
		}
		return apierrors.IsNotFound(k8s.Get(t.Context(), key, &infrav1.CompositeCluster{}))
	}, 10*time.Second, 100*time.Millisecond)
}
