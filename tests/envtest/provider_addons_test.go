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
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func addonProvider(t *testing.T, cc *infrav1.CompositeCluster, templateName, name string, overrides ...map[string]any) *unstructured.Unstructured {
	t.Helper()
	p := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": infrav1.GroupVersion.String(), "kind": "CompositeProvider",
		"metadata": map[string]any{"name": name, "namespace": cc.Namespace},
		"spec": map[string]any{
			"clusterRef":  map[string]any{"name": cc.Name},
			"templateRef": map[string]any{"name": templateName},
		},
	}}
	spec, ok := p.Object["spec"].(map[string]any)
	require.True(t, ok)
	for _, override := range overrides {
		maps.Copy(spec, override)
	}
	require.NoError(t, k8s.Create(t.Context(), p))
	return p
}

func getAddon(t *testing.T, p client.Object) *infrav1.CompositeProvider {
	t.Helper()
	fresh := &infrav1.CompositeProvider{}
	require.NoError(t, k8s.Get(t.Context(), client.ObjectKeyFromObject(p), fresh))
	return fresh
}

func TestProviderAddon_CredentialsAndProtectedOverrides(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "injection", true: "protected-override"}[rejected], func(t *testing.T) {
			template := regionalProfileDefinition()
			template.Spec.CredentialsNamePaths = []infrav1.ProfileJSONPointer{"/identityRef/name"}
			createRegionalProfile(t, k8s, template)
			protectRegionalProfile(t, template)
			cluster := newCAPICluster(t, newNamespace(t), "credentials-addon")
			cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
			input := map[string]any{"credentialsRef": map[string]any{"name": "tenant-credentials"}}
			if rejected {
				input["infraSpec"] = map[string]any{"identityRef": map[string]any{"name": "SECRET-CANARY"}}
			}
			p := addonProvider(t, cc, template.Name, "paris", input)
			_, cc, err := reconcileCluster(t, newClusterReconciler(), cc)
			require.NoError(t, err)
			requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
			if rejected {
				fresh := getAddon(t, p)
				requireCondition(t, fresh.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.OverrideRejectedReason)
				assert.NotContains(t, fresh.Status.Conditions[0].Message, "SECRET-CANARY")
				assert.Empty(t, fresh.Finalizers)
				assert.Empty(t, cc.Annotations[infrav1.ProviderAttachmentsAnnotation])
				assert.Nil(t, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name))
				return
			}
			child := getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name)
			require.NotNil(t, child)
			name, _, err := unstructured.NestedString(child.Object, "spec", "identityRef", "name")
			require.NoError(t, err)
			assert.Equal(t, "tenant-credentials", name)
			source := getUnstructured(
				t,
				schemaForSource(template.Spec.InfrastructureTemplateRef),
				template.Spec.InfrastructureTemplateRef.Namespace,
				template.Spec.InfrastructureTemplateRef.Name,
			)
			name, _, err = unstructured.NestedString(source.Object, "spec", "template", "spec", "identityRef", "name")
			require.NoError(t, err)
			assert.Equal(t, "fixed", name, "native sources are never modified")
			fresh := getAddon(t, p)
			assert.Contains(t, fresh.Finalizers, infrav1.ProviderAttachmentFinalizer)
			assert.Equal(t, cc.UID, metav1.GetControllerOf(fresh).UID)
		})
	}
}

func schemaForSource(ref infrav1.ProviderTemplateReference) schema.GroupVersionKind {
	return schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind)
}

func TestProviderAddon_CollisionFailsBeforeWrites(t *testing.T) {
	template := newRegionalProfile(t)
	cluster := newCAPICluster(t, newNamespace(t), "collision-addon")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	first := addonProvider(t, cc, template.Name, "first")
	second := addonProvider(t, cc, template.Name, "second")
	_, cc, err := reconcileCluster(t, newClusterReconciler(), cc)
	require.NoError(t, err)
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	assert.Empty(t, cc.Annotations[infrav1.ProviderAttachmentsAnnotation])
	assert.Empty(t, getAddon(t, first).Finalizers)
	assert.Empty(t, getAddon(t, second).Finalizers)
	assert.Nil(t, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name))
}

func TestProviderAddon_SourceDriftRetainsChildren(t *testing.T) {
	template := newRegionalProfile(t)
	cluster := newCAPICluster(t, newNamespace(t), "drift-addon")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	p := addonProvider(t, cc, template.Name, "paris")
	r := newClusterReconciler()
	_, cc, err := reconcileCluster(t, r, cc)
	require.NoError(t, err)
	child := getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name)
	source := getUnstructured(
		t,
		schemaForSource(template.Spec.InfrastructureTemplateRef),
		template.Spec.InfrastructureTemplateRef.Namespace,
		template.Spec.InfrastructureTemplateRef.Name,
	)
	require.NoError(t, unstructured.SetNestedField(source.Object, int64(2), "spec", "template", "spec", "replicaCount"))
	require.NoError(t, k8s.Update(t.Context(), source))
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	requireCondition(t, getAddon(t, p).Status.Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.ProfileMismatchReason)
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	assert.Equal(t, child.Object, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name).Object)
}

func TestProviderAddon_WorkersDrainBeforeSourceIndependentTeardown(t *testing.T) {
	template := newRegionalProfile(t)
	cluster := newCAPICluster(t, newNamespace(t), "teardown-addon")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	setEndpoint(t, cc, cluster, "192.0.2.40", 6443)
	p := addonProvider(t, cc, template.Name, "paris")
	r := newClusterReconciler()
	_, cc, err := reconcileCluster(t, r, cc)
	require.NoError(t, err)
	bootstrapName := cc.Name + "-paris"
	first := addonWorkerPool(t, cc.Namespace, cluster.Name, "small", bootstrapName)
	second := addonWorkerPool(t, cc.Namespace, cluster.Name, "medium", bootstrapName)
	source := getUnstructured(
		t,
		schemaForSource(template.Spec.InfrastructureTemplateRef),
		template.Spec.InfrastructureTemplateRef.Namespace,
		template.Spec.InfrastructureTemplateRef.Name,
	)
	require.NoError(t, k8s.Delete(t.Context(), source))
	require.NoError(t, k8s.Delete(t.Context(), p))
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	assert.NotNil(t, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name))
	requireCondition(t, getAddon(t, p).Status.Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.DeletingReason)
	require.NoError(t, k8s.Delete(t.Context(), first))
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	assert.NotNil(t, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name), "another pool still consumes the provider")
	require.NoError(t, k8s.Delete(t.Context(), second))
	child := getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name)
	child.SetFinalizers([]string{"tests.example.org/native-cleanup"})
	require.NoError(t, k8s.Update(t.Context(), child))
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	child = getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name)
	require.NotNil(t, child.GetDeletionTimestamp())
	assert.NotNil(t, getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, bootstrapName), "bootstrap remains until infrastructure is absent")
	child.SetFinalizers(nil)
	require.NoError(t, k8s.Update(t.Context(), child))
	key := client.ObjectKeyFromObject(p)
	require.Eventually(t, func() bool {
		_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cc)})
		return err == nil && apierrors.IsNotFound(k8s.Get(t.Context(), key, &infrav1.CompositeProvider{}))
	}, 5*time.Second, 50*time.Millisecond)
	assert.Nil(t, getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, bootstrapName))
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	version := cc.ResourceVersion
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	assert.Equal(t, version, cc.ResourceVersion, "settled infrastructure is write-free")
}

func addonWorkerPool(t *testing.T, namespace, clusterName, name, bootstrap string) *unstructured.Unstructured {
	t.Helper()
	md := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": clusterGVK.GroupVersion().String(), "kind": "MachineDeployment",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"clusterName": clusterName, "replicas": int64(1), "selector": map[string]any{},
			"template": map[string]any{"spec": map[string]any{
				"clusterName": clusterName,
				"version":     "v1.34.1",
				"bootstrap": map[string]any{
					"configRef": map[string]any{"apiGroup": "bootstrap.cluster.x-k8s.io", "kind": "KubeadmConfigTemplate", "name": bootstrap},
				},
				"infrastructureRef": map[string]any{"apiGroup": fakeClusterGVK.Group, "kind": "FakeMachineTemplate", "name": "native-workers"},
			}},
		},
	}}
	require.NoError(t, k8s.Create(t.Context(), md))
	return md
}

func TestProviderAddon_IndependentReadinessAndCredentials(t *testing.T) {
	template := newRegionalProfile(t)
	cluster := newCAPICluster(t, newNamespace(t), "addon-consumer")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	r := newClusterReconciler()
	_, cc, err := reconcileCluster(t, r, cc)
	require.NoError(t, err)
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	p := addonProvider(t, cc, template.Name, "paris")
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	require.NotNil(t, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name))
	assert.Nil(t, getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, cc.Name+"-paris"), "bootstrap waits for an endpoint")
	setEndpoint(t, cc, cluster, "192.0.2.50", 6443)
	markFakeClusterReady(t, cc.Namespace, cc.Name)
	_, cc, err = reconcileCluster(t, r, cc)
	require.NoError(t, err)
	fresh := getAddon(t, p)
	requireCondition(t, fresh.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	bootstrap := fresh.Status.BootstrapTemplateRef.Name
	assert.Equal(t, cc.Name+"-paris", bootstrap)
	require.NotNil(t, getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, bootstrap))
}

func TestProviderAddon_InvalidTemplateDoesNotBlockCluster(t *testing.T) {
	cluster := newCAPICluster(t, newNamespace(t), "pending-addon")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	p := addonProvider(t, cc, "missing-template", "paris")
	_, cc, err := reconcileCluster(t, newClusterReconciler(), cc)
	require.NoError(t, err)
	requireCondition(t, cc.Status.Conditions, infrav1.ReadyCondition, metav1.ConditionTrue, infrav1.ReadyReason)
	requireCondition(t, getAddon(t, p).Status.Conditions, infrav1.ReadyCondition, metav1.ConditionFalse, infrav1.ProfileUnavailableReason)
	assert.Nil(t, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name))
}

func TestProviderAddon_APIIsNamespaced(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, k8s.Get(t.Context(), client.ObjectKey{Name: "compositeproviders.infrastructure.cluster.x-k8s.io"}, crd))
	assert.Equal(t, apiextensionsv1.NamespaceScoped, crd.Spec.Scope)
}

func TestProviderAddon_ImmutableReferences(t *testing.T) {
	for _, field := range []string{"clusterRef", "templateRef", "credentialsRef", "infraClusterName"} {
		t.Run(field, func(t *testing.T) {
			cluster := newCAPICluster(t, newNamespace(t), "immutable-addon")
			cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
			p := addonProvider(t, cc, "template-v1", "paris", map[string]any{"credentialsRef": map[string]any{"name": "credentials"}})
			var value any = map[string]any{"name": "replacement"}
			if field == "infraClusterName" {
				value = "different-infrastructure"
			}
			require.NoError(t, unstructured.SetNestedField(p.Object, value, "spec", field))
			err := k8s.Update(t.Context(), p)
			require.True(t, apierrors.IsInvalid(err), "%v", err)
			assert.ErrorContains(t, err, field+" is immutable")
		})
	}
}

func TestProviderAddon_ClusterDeletionDoesNotNeedSources(t *testing.T) {
	template := newRegionalProfile(t)
	cluster := newCAPICluster(t, newNamespace(t), "deleted-contract")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	setEndpoint(t, cc, cluster, "192.0.2.60", 6443)
	p := addonProvider(t, cc, template.Name, "paris")
	r := newClusterReconciler()
	_, cc, err := reconcileCluster(t, r, cc)
	require.NoError(t, err)
	bootstrap := cc.Name + "-paris"
	source := getUnstructured(
		t,
		schemaForSource(template.Spec.InfrastructureTemplateRef),
		template.Spec.InfrastructureTemplateRef.Namespace,
		template.Spec.InfrastructureTemplateRef.Name,
	)
	require.NoError(t, k8s.Delete(t.Context(), source))
	deleteProfileConsumer(t, r, cc)
	assert.Nil(t, getUnstructured(t, fakeClusterGVK, cc.Namespace, cc.Name))
	assert.Nil(t, getUnstructured(t, kubeadmConfigTemplateGVK, cc.Namespace, bootstrap))
	assert.True(t, apierrors.IsNotFound(k8s.Get(t.Context(), client.ObjectKeyFromObject(p), &infrav1.CompositeProvider{})))
}
