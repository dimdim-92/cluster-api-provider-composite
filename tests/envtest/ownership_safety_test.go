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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
)

type ownershipRaceClient struct {
	client.Client
	getError     func(client.Object) error
	beforeCreate func()
	beforeWrite  func([]byte)
	beforeDelete func(client.Object, ...client.DeleteOption)
	getErrors    []error
	createErrors []error
	patchCalls   int // Both Patch and Apply issue PATCH requests.
	deleteCalls  int
}

func (c *ownershipRaceClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.getError != nil {
		if err := c.getError(obj); err != nil {
			return err
		}
	}
	err := c.Client.Get(ctx, key, obj, opts...)
	c.getErrors = append(c.getErrors, err)
	if err != nil {
		return fmt.Errorf("race client get: %w", err)
	}
	return nil
}

func (c *ownershipRaceClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if c.beforeCreate != nil {
		before := c.beforeCreate
		c.beforeCreate = nil
		before()
	}
	err := c.Client.Create(ctx, obj, opts...)
	c.createErrors = append(c.createErrors, err)
	if err != nil {
		return fmt.Errorf("race client create: %w", err)
	}
	return nil
}

func (c *ownershipRaceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.patchCalls++
	if c.beforeWrite != nil {
		data, err := patch.Data(obj)
		if err != nil {
			return fmt.Errorf("encoding race client patch: %w", err)
		}
		before := c.beforeWrite
		c.beforeWrite = nil
		before(data)
	}
	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return fmt.Errorf("race client patch: %w", err)
	}
	return nil
}

func (c *ownershipRaceClient) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	c.patchCalls++
	if c.beforeWrite != nil {
		data, err := json.Marshal(obj)
		if err != nil {
			return fmt.Errorf("encoding race client apply: %w", err)
		}
		before := c.beforeWrite
		c.beforeWrite = nil
		before(data)
	}
	if err := c.Client.Apply(ctx, obj, opts...); err != nil {
		return fmt.Errorf("race client apply: %w", err)
	}
	return nil
}

func (c *ownershipRaceClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.deleteCalls++
	if c.beforeDelete != nil {
		before := c.beforeDelete
		c.beforeDelete = nil
		before(obj, opts...)
	}
	if err := c.Client.Delete(ctx, obj, opts...); err != nil {
		return fmt.Errorf("race client delete: %w", err)
	}
	return nil
}

func assertChildUnchanged(t *testing.T, before *unstructured.Unstructured) {
	t.Helper()
	after := getUnstructured(t, before.GroupVersionKind(), before.GetNamespace(), before.GetName())
	require.NotNil(t, after, "a rejected object must survive reconcile and teardown")
	assert.Nil(t, after.GetDeletionTimestamp())
	assert.Equal(t, before.GetUID(), after.GetUID())
	assert.Equal(t, before.GetResourceVersion(), after.GetResourceVersion())
	assert.Equal(t, before.Object["spec"], after.Object["spec"])
	assert.Equal(t, before.GetOwnerReferences(), after.GetOwnerReferences())
}

func TestApplyChild_RejectsUnownedAndConflictingCluster(t *testing.T) {
	for _, kind := range []string{"infrastructure", "bootstrap"} {
		for _, scenario := range []string{"unowned", "foreign-cluster", "ours-with-foreign-cluster"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				ns := newNamespace(t)
				cluster := newCAPICluster(t, ns, "expected")
				other := newCAPICluster(t, ns, "other")
				cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
				gvk, name := fakeClusterGVK, cc.Name
				spec, desired := map[string]any{"replicaCount": int64(99)}, map[string]any{"replicaCount": int64(1)}
				if kind == "bootstrap" {
					gvk, name = kubeadmConfigTemplateGVK, cc.Name+"-fake"
					spec = map[string]any{"template": map[string]any{"spec": map[string]any{"preKubeadmCommands": []any{"true"}}}}
					desired = map[string]any{"template": map[string]any{"spec": map[string]any{"preKubeadmCommands": []any{"false"}}}}
				}
				foreign := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
				foreign.SetGroupVersionKind(gvk)
				foreign.SetName(name)
				foreign.SetNamespace(ns)
				var refs []metav1.OwnerReference
				if scenario != "unowned" {
					refs = append(refs, metav1.OwnerReference{
						APIVersion: clusterv1.GroupVersion.String(), Kind: "Cluster",
						Name: other.Name, UID: other.UID,
					})
				}
				if scenario == "ours-with-foreign-cluster" {
					refs = append(refs, *metav1.NewControllerRef(cc, infrav1.GroupVersion.WithKind("CompositeCluster")))
				}
				foreign.SetOwnerReferences(refs)
				require.NoError(t, k8s.Create(t.Context(), foreign))
				before := foreign.DeepCopy()
				c := &ownershipRaceClient{Client: k8s}

				for range 3 {
					_, err := capi.ApplyChild(t.Context(), c, gvk, name, ns, desired,
						capi.Ownership{Controller: cc, Cluster: cluster})
					require.ErrorIs(t, err, capi.ErrChildNotOwned)
					assertChildUnchanged(t, before)
					assert.Zero(t, c.patchCalls, "rejected ownership must not reach patch")
				}
			})
		}
	}
}

func unboundChildRejectionReason(scenario string) string {
	if scenario == "ours-with-foreign-cluster" {
		// Already-owned objects without attachment pins cannot be reconstructed.
		return infrav1.ProfileMismatchReason
	}
	return infrav1.ChildNotOwnedReason
}

func TestCompositeClusterReconciler_DoesNotDeleteForeignObjects(t *testing.T) {
	for _, kind := range []string{"infrastructure", "bootstrap"} {
		for _, scenario := range []string{"foreign-controller", "unowned", "foreign-cluster", "ours-with-foreign-cluster"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				ns := newNamespace(t)
				r := newClusterReconciler()
				cluster := newCAPICluster(t, ns, "expected")
				cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
				template := newRegionalProfile(t)
				provider := createProviderAttachment(t, cc, template.Name, "fake")
				otherCluster := newCAPICluster(t, ns, "other")
				setEndpoint(t, cc, cluster, "203.0.113.10", 6443)

				gvk, name := fakeClusterGVK, cc.Name
				spec := map[string]any{"replicaCount": int64(99)}
				if kind == "bootstrap" {
					gvk, name = kubeadmConfigTemplateGVK, cc.Name+"-fake"
					spec = map[string]any{"template": map[string]any{"spec": map[string]any{
						"preKubeadmCommands": []any{"true"},
					}}}
				}
				foreign := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
				foreign.SetGroupVersionKind(gvk)
				foreign.SetName(name)
				foreign.SetNamespace(ns)
				foreign.SetLabels(map[string]string{
					clusterv1.ClusterNameLabel: cluster.Name,
					infrav1.ProviderNameLabel:  "fake",
				})
				switch scenario {
				case "foreign-controller":
					other := newCompositeCluster(t, otherCluster, infrav1.CompositeClusterSpec{})
					foreign.SetOwnerReferences([]metav1.OwnerReference{
						*metav1.NewControllerRef(other, infrav1.GroupVersion.WithKind("CompositeCluster")),
						{APIVersion: clusterv1.GroupVersion.String(), Kind: "Cluster", Name: cluster.Name, UID: cluster.UID},
					})
				case "foreign-cluster", "ours-with-foreign-cluster":
					refs := []metav1.OwnerReference{{
						APIVersion: clusterv1.GroupVersion.String(), Kind: "Cluster",
						Name: otherCluster.Name, UID: otherCluster.UID,
					}}
					if scenario == "ours-with-foreign-cluster" {
						refs = append(refs, *metav1.NewControllerRef(cc, infrav1.GroupVersion.WithKind("CompositeCluster")))
					}
					foreign.SetOwnerReferences(refs)
				}
				require.NoError(t, k8s.Create(t.Context(), foreign))
				before := foreign.DeepCopy()
				reason := unboundChildRejectionReason(scenario)

				for range 3 {
					_, current, err := reconcileCluster(t, r, cc)
					require.NoError(t, err)
					require.NotNil(t, current)
					cc = current
					requireCondition(
						t,
						getProviderAttachment(t, provider).Status.Conditions,
						infrav1.ReadyCondition,
						metav1.ConditionFalse,
						reason,
					)
					assertChildUnchanged(t, before)
				}
				require.Contains(t, cc.Finalizers, infrav1.ClusterFinalizer)
				require.NoError(t, k8s.Delete(t.Context(), cc))
				for range 3 {
					_, _, err := reconcileCluster(t, r, cc)
					if err != nil {
						require.ErrorIs(t, err, capi.ErrChildNotOwned)
					}
					assertChildUnchanged(t, before)
				}
			})
		}
	}
}

func TestApplyChild_ConcurrentCreateDoesNotAdopt(t *testing.T) {
	for _, scenario := range []string{"unowned", "foreign-controller"} {
		t.Run(scenario, func(t *testing.T) {
			ns := newNamespace(t)
			cluster := newCAPICluster(t, ns, "race")
			cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
			foreign := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"replicaCount": int64(99)}}}
			foreign.SetGroupVersionKind(fakeClusterGVK)
			foreign.SetName(cc.Name)
			foreign.SetNamespace(ns)
			if scenario == "foreign-controller" {
				other := newCompositeCluster(t, newCAPICluster(t, ns, "other"), infrav1.CompositeClusterSpec{})
				foreign.SetOwnerReferences([]metav1.OwnerReference{
					*metav1.NewControllerRef(other, infrav1.GroupVersion.WithKind("CompositeCluster")),
					{APIVersion: clusterv1.GroupVersion.String(), Kind: "Cluster", Name: cluster.Name, UID: cluster.UID},
				})
			}
			c := &ownershipRaceClient{Client: k8s, beforeCreate: func() {
				require.NoError(t, k8s.Create(t.Context(), foreign))
			}}
			own := capi.Ownership{Controller: cc, Cluster: cluster}
			_, err := capi.ApplyChild(t.Context(), c, fakeClusterGVK, cc.Name, ns,
				map[string]any{"replicaCount": int64(1)}, own)
			require.ErrorIs(t, err, capi.ErrChildNotOwned)
			require.Len(t, c.createErrors, 1)
			assert.True(t, apierrors.IsAlreadyExists(c.createErrors[0]), "atomic create must lose to the foreign winner")
			require.Len(t, c.getErrors, 2, "AlreadyExists must trigger a reread")
			assert.True(t, apierrors.IsNotFound(c.getErrors[0]), "the initial read must observe absence")
			assert.NoError(t, c.getErrors[1], "the reread must observe the winner")
			assert.Zero(t, c.patchCalls)
			before := foreign.DeepCopy()
			assertChildUnchanged(t, before)

			_, err = capi.ApplyChild(t.Context(), c, fakeClusterGVK, cc.Name, ns,
				map[string]any{"replicaCount": int64(1)}, own)
			require.ErrorIs(t, err, capi.ErrChildNotOwned)
			assert.Len(t, c.createErrors, 1, "retry must not attempt another create")
			assert.Len(t, c.getErrors, 3, "retry must reread ownership")
			assert.Zero(t, c.patchCalls, "retry must not patch the winner")
			assertChildUnchanged(t, before)
		})
	}
}

func TestApplyChild_ConcurrentOwnerChangeIsRejected(t *testing.T) {
	for _, afterHandoff := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-handoff=%t", afterHandoff), func(t *testing.T) {
			testConcurrentOwnerChange(t, afterHandoff)
		})
	}
}

func testConcurrentOwnerChange(t *testing.T, afterHandoff bool) {
	t.Helper()
	ns := newNamespace(t)
	cluster := newCAPICluster(t, ns, "race")
	cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
	own := capi.Ownership{Controller: cc, Cluster: cluster}
	_, err := capi.ApplyChild(t.Context(), k8s, fakeClusterGVK, cc.Name, ns, map[string]any{"replicaCount": int64(1)}, own)
	require.NoError(t, err)
	if afterHandoff {
		_, err = capi.ApplyChild(t.Context(), k8s, fakeClusterGVK, cc.Name, ns, map[string]any{"replicaCount": int64(1)}, own)
		require.NoError(t, err)
	}
	original := getUnstructured(t, fakeClusterGVK, ns, cc.Name)
	require.NotNil(t, original)
	var changed *unstructured.Unstructured
	c := &ownershipRaceClient{Client: k8s, beforeWrite: func(data []byte) {
		var sent unstructured.Unstructured
		require.NoError(t, json.Unmarshal(data, &sent))
		assert.Equal(t, original.GetUID(), sent.GetUID(), "the actual patch must guard object identity")
		assert.Equal(t, original.GetResourceVersion(), sent.GetResourceVersion(), "the actual patch must guard the ownership read")
		child := getUnstructured(t, fakeClusterGVK, ns, cc.Name)
		require.NotNil(t, child)
		child.SetOwnerReferences(nil)
		require.NoError(t, k8s.Update(t.Context(), child))
		changed = child.DeepCopy()
	}}
	_, err = capi.ApplyChild(t.Context(), c, fakeClusterGVK, cc.Name, ns, map[string]any{"replicaCount": int64(99)}, own)
	require.True(t, apierrors.IsConflict(err), "expected an API-server precondition conflict, got %v", err)
	require.Equal(t, 1, c.patchCalls)
	require.NotNil(t, changed)
	assert.Equal(t, original.GetUID(), changed.GetUID(), "only ownership, not identity, changed")
	assert.NotEqual(t, original.GetResourceVersion(), changed.GetResourceVersion())
	assert.Empty(t, changed.GetOwnerReferences())
	assert.Equal(t, original.Object["spec"], changed.Object["spec"])
	assertChildUnchanged(t, changed)

	_, err = capi.ApplyChild(t.Context(), c, fakeClusterGVK, cc.Name, ns, map[string]any{"replicaCount": int64(99)}, own)
	require.ErrorIs(t, err, capi.ErrChildNotOwned)
	assert.Len(t, c.getErrors, 2, "retry must reread the changed ownership")
	assert.Equal(t, 1, c.patchCalls, "retry must refuse ownership before a second patch")
	assertChildUnchanged(t, changed)
}

func TestCompositeClusterReconciler_DeleteDoesNotTargetReplacement(t *testing.T) {
	for _, kind := range []string{"infrastructure", "bootstrap"} {
		t.Run(kind, func(t *testing.T) {
			ns := newNamespace(t)
			r := newClusterReconciler()
			cluster := newCAPICluster(t, ns, "replacement")
			cc := newCompositeCluster(t, cluster, infrav1.CompositeClusterSpec{})
			createProviderAttachment(t, cc, newRegionalProfile(t).Name, "fake")
			setEndpoint(t, cc, cluster, "203.0.113.10", 6443)
			_, cc, err := reconcileCluster(t, r, cc)
			require.NoError(t, err)
			require.NotNil(t, cc)
			require.NoError(t, k8s.Delete(t.Context(), cc))

			gvk, name := fakeClusterGVK, cc.Name
			spec := map[string]any{"replicaCount": int64(99)}
			if kind == "bootstrap" {
				// Finish the infrastructure deletion before interleaving the template delete.
				_, current, err := reconcileCluster(t, r, cc)
				require.NoError(t, err)
				require.NotNil(t, current)
				cc = current
				require.Nil(t, getUnstructured(t, fakeClusterGVK, ns, cc.Name))
				gvk, name = kubeadmConfigTemplateGVK, cc.Name+"-fake"
				spec = map[string]any{"template": map[string]any{"spec": map[string]any{"preKubeadmCommands": []any{"true"}}}}
			}
			original := getUnstructured(t, gvk, ns, name)
			require.NotNil(t, original)
			replacement := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
			replacement.SetGroupVersionKind(gvk)
			replacement.SetName(name)
			replacement.SetNamespace(ns)
			c := &ownershipRaceClient{Client: k8s, beforeDelete: func(obj client.Object, opts ...client.DeleteOption) {
				assert.Equal(t, gvk, obj.GetObjectKind().GroupVersionKind())
				assert.Equal(t, client.ObjectKeyFromObject(original), client.ObjectKeyFromObject(obj))
				options := &client.DeleteOptions{}
				for _, opt := range opts {
					opt.ApplyToDelete(options)
				}
				require.NotNil(t, options.Preconditions)
				require.NotNil(t, options.Preconditions.UID)
				require.NotNil(t, options.Preconditions.ResourceVersion)
				assert.Equal(t, original.GetUID(), *options.Preconditions.UID)
				assert.Equal(t, original.GetResourceVersion(), *options.Preconditions.ResourceVersion)
				require.NoError(t, k8s.Delete(t.Context(), original))
				require.NoError(t, k8s.Create(t.Context(), replacement))
			}}
			r.Client = c
			_, current, err := reconcileCluster(t, r, cc)
			require.True(t, apierrors.IsConflict(err), "expected a delete precondition conflict, got %v", err)
			require.NotNil(t, current)
			assert.Contains(t, current.Finalizers, infrav1.ClusterFinalizer)
			assert.Equal(t, 1, c.deleteCalls)
			before := replacement.DeepCopy()
			assert.NotEqual(t, original.GetUID(), before.GetUID())
			assertChildUnchanged(t, before)

			for range 3 {
				_, current, err = reconcileCluster(t, r, cc)
				require.ErrorIs(t, err, capi.ErrChildNotOwned)
				require.NotNil(t, current)
				assert.Contains(t, current.Finalizers, infrav1.ClusterFinalizer)
				assert.Equal(t, 1, c.deleteCalls, "retry must not delete the replacement")
				assertChildUnchanged(t, before)
			}
		})
	}
}

func TestCompositeClusterReconciler_MissingAPIIsNotMissingChild(t *testing.T) {
	ns := newNamespace(t)
	r := newClusterReconciler()
	cc := newCompositeCluster(t, newCAPICluster(t, ns, "missing-api"), infrav1.CompositeClusterSpec{})
	createProviderAttachment(t, cc, newRegionalProfile(t).Name, "fake")
	_, cc, err := reconcileCluster(t, r, cc)
	require.NoError(t, err)
	r.Client = &ownershipRaceClient{Client: k8s, getError: func(obj client.Object) error {
		if obj.GetObjectKind().GroupVersionKind() != fakeClusterGVK {
			return nil
		}
		return &apierrors.StatusError{ErrStatus: metav1.Status{
			Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: 404,
			Message: "the server could not find the requested resource",
		}}
	}}
	require.NoError(t, k8s.Delete(t.Context(), cc))
	_, current, err := reconcileCluster(t, r, cc)
	require.Error(t, err, "a missing API endpoint is not evidence that its child was deleted")
	require.NotNil(t, current)
	assert.Contains(t, current.Finalizers, infrav1.ClusterFinalizer)
	child := getUnstructured(t, fakeClusterGVK, ns, cc.Name)
	require.NotNil(t, child)
	assert.Nil(t, child.GetDeletionTimestamp())
}
