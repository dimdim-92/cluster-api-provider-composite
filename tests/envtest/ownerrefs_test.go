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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
)

// M0 spike 3.
//
// The whole delegation design rests on one piece of upstream behaviour: that
// util.GetOwnerCluster finds a Cluster through a NON-controller ownerReference.
// If it required the controller ownerRef, we could not both own our children
// (for Owns() watches and GC) and let the upstream provider find its Cluster --
// an object may have only one controller owner.
//
// These tests assert that against Cluster API's real util.GetOwnerCluster rather
// than a local reimplementation of it, because a reimplementation would only
// prove we agree with ourselves. See
// docs/design.md.

var fakeClusterGVK = schema.GroupVersionKind{
	Group:   "infrastructure.cluster.x-k8s.io",
	Version: "v1beta1",
	Kind:    "FakeCluster",
}

// newOwners creates a CAPI Cluster and a CompositeCluster to own a child, named
// off the test so cases do not collide.
func newOwners(t *testing.T, name string) (*clusterv1.Cluster, *infrav1.CompositeCluster) {
	t.Helper()
	namespace := newNamespace(t)

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		// The CRD requires a non-empty spec, and a zero ClusterSpec is dropped by
		// omitzero. Pointing infrastructureRef at the CompositeCluster is both
		// non-zero and the real production shape.
		Spec: clusterv1.ClusterSpec{
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "CompositeCluster",
				Name:     name,
			},
		},
	}
	require.NoError(t, k8s.Create(ctx(t), cluster))

	cc := &infrav1.CompositeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	require.NoError(t, k8s.Create(ctx(t), cc))

	// ApplyChild builds the ownerRef from the object's GVK, which a typed Create
	// leaves empty on the returned object.
	cc.SetGroupVersionKind(infrav1.GroupVersion.WithKind("CompositeCluster"))

	return cluster, cc
}

func getChild(t *testing.T, namespace, name string) *unstructured.Unstructured {
	t.Helper()

	child := &unstructured.Unstructured{}
	child.SetGroupVersionKind(fakeClusterGVK)
	require.NoError(t, k8s.Get(ctx(t), types.NamespacedName{Name: name, Namespace: namespace}, child))

	return child
}

// TestApplyChild_StampsBothOwnerReferences is the shape assertion: two refs,
// exactly one of them the controller, and it must be ours.
func TestApplyChild_StampsBothOwnerReferences(t *testing.T) {
	name := "ownerrefs-shape"
	cluster, cc := newOwners(t, name)

	_, err := capi.ApplyChild(ctx(t), k8s, fakeClusterGVK, name, cc.Namespace,
		map[string]any{"placeholder": "x"},
		capi.Ownership{
			Controller: cc,
			Cluster:    cluster,
			Labels:     map[string]string{clusterv1.ClusterNameLabel: cluster.Name},
		})
	require.NoError(t, err)

	child := getChild(t, cc.Namespace, name)
	refs := child.GetOwnerReferences()
	require.Len(t, refs, 2, "a child must carry both the Cluster and the CompositeCluster ownerRef")

	byKind := map[string]metav1.OwnerReference{}
	for _, r := range refs {
		byKind[r.Kind] = r
	}

	clusterRef, ok := byKind["Cluster"]
	require.True(t, ok, "missing the Cluster ownerRef that upstream resolves through")
	assert.NotEqual(t, true, ptrBool(clusterRef.Controller),
		"the Cluster ownerRef must NOT be the controller; only one ref may be, and it has to be ours")

	ccRef, ok := byKind["CompositeCluster"]
	require.True(t, ok, "missing our own ownerRef")
	assert.True(t, ptrBool(ccRef.Controller), "our ownerRef must be the controller, for Owns() and GC")

	assert.Equal(t, cluster.Name, child.GetLabels()[clusterv1.ClusterNameLabel])
}

// TestApplyChild_UpstreamResolvesOwnerCluster is the spike itself: Cluster API's
// own helper, unmodified, must find the Cluster through the non-controller ref.
func TestApplyChild_UpstreamResolvesOwnerCluster(t *testing.T) {
	name := "ownerrefs-upstream"
	cluster, cc := newOwners(t, name)

	_, err := capi.ApplyChild(ctx(t), k8s, fakeClusterGVK, name, cc.Namespace,
		map[string]any{"placeholder": "x"},
		capi.Ownership{Controller: cc, Cluster: cluster})
	require.NoError(t, err)

	child := getChild(t, cc.Namespace, name)

	owner, err := util.GetOwnerCluster(ctx(t), k8s, metav1.ObjectMeta{
		Namespace:       child.GetNamespace(),
		OwnerReferences: child.GetOwnerReferences(),
	})
	require.NoError(t, err)
	require.NotNil(t, owner,
		"util.GetOwnerCluster must resolve through a non-controller ownerRef; "+
			"if this fails the whole delegation design is unworkable")
	assert.Equal(t, cluster.Name, owner.Name)
}

// TestApplyChild_WithoutClusterRefUpstreamFindsNothing verifies the silent lookup
// failure prevented by dual ownership: no error, just a nil Cluster.
func TestApplyChild_WithoutClusterRefUpstreamFindsNothing(t *testing.T) {
	name := "ownerrefs-missing"
	_, cc := newOwners(t, name)

	_, err := capi.ApplyChild(ctx(t), k8s, fakeClusterGVK, name, cc.Namespace,
		map[string]any{"placeholder": "x"},
		capi.Ownership{Controller: cc}) // Cluster deliberately omitted
	require.NoError(t, err, "the apply itself succeeds -- that is what makes this silent")

	child := getChild(t, cc.Namespace, name)
	require.Len(t, child.GetOwnerReferences(), 1)

	owner, err := util.GetOwnerCluster(ctx(t), k8s, metav1.ObjectMeta{
		Namespace:       child.GetNamespace(),
		OwnerReferences: child.GetOwnerReferences(),
	})
	require.NoError(t, err, "upstream returns no error either -- it just finds nothing")
	assert.Nil(t, owner,
		"without the Cluster ownerRef the provider would no-op forever, with no error, "+
			"event or condition to explain why")
}

// TestApplyChild_RequiresControllerOwner requires explicit Composite controller
// ownership before writing, in addition to the native provider's Cluster reference.
func TestApplyChild_RequiresControllerOwner(t *testing.T) {
	cluster, _ := newOwners(t, "ownerrefs-nocontroller")

	_, err := capi.ApplyChild(ctx(t), k8s, fakeClusterGVK, "ownerrefs-nocontroller", cluster.Namespace,
		map[string]any{"placeholder": "x"},
		capi.Ownership{Cluster: cluster})
	require.Error(t, err)
	assert.ErrorIs(t, err, capi.ErrNoControllerOwner)
}

// TestApplyChild_RefusesForeignChild: adopting an object controlled by something
// else could hand another controller's resources to a tenant.
func TestApplyChild_RefusesForeignChild(t *testing.T) {
	name := "ownerrefs-foreign"
	cluster, cc := newOwners(t, name)

	foreign := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}
	foreign.SetGroupVersionKind(fakeClusterGVK)
	foreign.SetName(name)
	foreign.SetNamespace(cc.Namespace)
	foreign.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: infrav1.GroupVersion.String(),
		Kind:       "CompositeCluster",
		Name:       "someone-else",
		UID:        types.UID("11111111-1111-1111-1111-111111111111"),
		Controller: new(true),
	}})
	require.NoError(t, k8s.Create(ctx(t), foreign))

	_, err := capi.ApplyChild(ctx(t), k8s, fakeClusterGVK, name, cc.Namespace,
		map[string]any{"placeholder": "x"},
		capi.Ownership{Controller: cc, Cluster: cluster})
	require.Error(t, err)
	assert.ErrorIs(t, err, capi.ErrChildNotOwned)
}

// TestApplyChild_UpdatesExistingChild covers the prototype defect where a child
// was created once and never reconciled again, so class edits silently did
// nothing.
func TestApplyChild_UpdatesExistingChild(t *testing.T) {
	name := "ownerrefs-update"
	cluster, cc := newOwners(t, name)
	own := capi.Ownership{Controller: cc, Cluster: cluster}

	_, err := capi.ApplyChild(ctx(t), k8s, fakeClusterGVK, name, cc.Namespace,
		map[string]any{"size": "small"}, own)
	require.NoError(t, err)

	_, err = capi.ApplyChild(ctx(t), k8s, fakeClusterGVK, name, cc.Namespace,
		map[string]any{"size": "large"}, own)
	require.NoError(t, err)

	child := getChild(t, cc.Namespace, name)
	got, _, err := unstructured.NestedString(child.Object, "spec", "size")
	require.NoError(t, err)
	assert.Equal(t, "large", got, "a second apply must update the child, not silently no-op")
}

// TestReadInfraCluster_V1Beta1ContractPath exercises the status path the real
// providers actually take today: both CAPOSC and CAPO serve the v1beta1
// contract, where readiness lives at status.ready rather than
// status.initialization.provisioned.
func TestReadInfraCluster_V1Beta1ContractPath(t *testing.T) {
	name := "ownerrefs-status"
	cluster, cc := newOwners(t, name)

	_, err := capi.ApplyChild(ctx(t), k8s, fakeClusterGVK, name, cc.Namespace,
		map[string]any{"placeholder": "x"},
		capi.Ownership{Controller: cc, Cluster: cluster})
	require.NoError(t, err)

	// Spec first. Update() returns the server's object with status stripped --
	// status is a subresource -- so setting status before this would be silently
	// discarded.
	child := getChild(t, cc.Namespace, name)
	require.NoError(t, unstructured.SetNestedMap(child.Object,
		map[string]any{"host": "10.0.0.1", "port": int64(6443)}, "spec", "controlPlaneEndpoint"))
	require.NoError(t, k8s.Update(ctx(t), child))

	require.NoError(t, unstructured.SetNestedField(child.Object, true, "status", "ready"))
	require.NoError(t, k8s.Status().Update(ctx(t), child))

	// The fake CRD advertises the v1beta1 contract, so the cache must select the
	// status.ready path.
	contract, err := capi.NewContractCache(k8s).Get(ctx(t), fakeClusterGVK.GroupKind())
	require.NoError(t, err)
	require.Equal(t, capi.ContractV1Beta1, contract.Contract)

	st, err := capi.ReadInfraCluster(getChild(t, cc.Namespace, name), contract.Contract)
	require.NoError(t, err)
	assert.True(t, st.Provisioned, "v1beta1 readiness must be read from status.ready")
	assert.Equal(t, "10.0.0.1", st.Endpoint.Host)
	assert.Equal(t, int32(6443), st.Endpoint.Port)
}

//go:fix inline

func ptrBool(p *bool) bool {
	if p == nil {
		return false
	}

	return *p
}
