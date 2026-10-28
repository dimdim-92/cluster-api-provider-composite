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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
	"github.com/numspot/cluster-api-provider-composite/internal/controller"
)

// Shared fixtures for the reconciler tests. The reconcilers are driven by
// calling Reconcile directly with the suite's live client: no manager, no
// watches, no cache — every step is deterministic and every object read is
// fresh. Watch wiring is covered by operator runtime tests; CAPI/CABPK/provider
// controllers and webhooks require separate native-stack integration.

// apiextensionsv1JSON shortens the opaque payload type in fixtures.
type apiextensionsv1JSON = apiextensionsv1.JSON

var (
	// clusterGVK is the version the reconcilers read Clusters at in these
	// tests. Production resolves it from the REST mapper; the envtest CRDs
	// (cluster-api module) store v1beta2.
	clusterGVK = clusterv1.GroupVersion.WithKind("Cluster")

	// The bootstrap wire version this provider writes: v1beta2, as stored by
	// the module's CRDs.
	kubeadmConfigTemplateGVK = schema.GroupVersionKind{
		Group: "bootstrap.cluster.x-k8s.io", Version: "v1beta2", Kind: "KubeadmConfigTemplate",
	}
)

// newClusterReconciler builds a CompositeClusterReconciler over the suite
// client, resolving native kinds from CompositeProvider templates.
func newClusterReconciler() *controller.CompositeClusterReconciler {
	return &controller.CompositeClusterReconciler{
		Client:    k8s,
		Contracts: capi.NewContractCache(k8s),
		Clusters:  controller.NewClusterReader(k8s, clusterGVK),
	}
}

// newNamespace creates a namespace unique to the test. Namespaces are not
// deleted (envtest has no namespace controller to finalize them), which is
// why every test gets its own.
func newNamespace(t *testing.T) string {
	t.Helper()

	prefix := sanitise(t.Name())
	if len(prefix) > 40 {
		prefix = prefix[:40]
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "t-" + prefix + "-"}}
	require.NoError(t, k8s.Create(ctx(t), ns))

	return ns.Name
}

// newCAPICluster creates a CAPI Cluster whose infrastructureRef names the
// CompositeCluster, exactly as the platform stamps it.
func newCAPICluster(t *testing.T, namespace, name string) *clusterv1.Cluster {
	t.Helper()

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: clusterv1.ClusterSpec{
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "CompositeCluster",
				Name:     name,
			},
		},
	}
	require.NoError(t, k8s.Create(ctx(t), cluster))

	return cluster
}

// newCompositeCluster creates a CompositeCluster owned by the CAPI Cluster —
// the ownerRef CAPI's Cluster controller stamps on the infrastructureRef
// target once it adopts it.
func newCompositeCluster(t *testing.T, cluster *clusterv1.Cluster, spec infrav1.CompositeClusterSpec) *infrav1.CompositeCluster {
	t.Helper()

	cc := &infrav1.CompositeCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Spec.InfrastructureRef.Name,
			Namespace: cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterGVK.GroupVersion().String(),
				Kind:       "Cluster",
				Name:       cluster.Name,
				UID:        cluster.UID,
				Controller: new(true),
			}},
		},
		Spec: spec,
	}
	require.NoError(t, k8s.Create(ctx(t), cc))

	return cc
}

// reconcileCluster runs one CompositeCluster reconcile and returns the
// refreshed object.
func reconcileCluster(
	t *testing.T,
	r *controller.CompositeClusterReconciler,
	cc *infrav1.CompositeCluster,
) (ctrl.Result, *infrav1.CompositeCluster, error) {
	t.Helper()

	res, err := r.Reconcile(ctx(t), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cc)})
	if err != nil {
		err = fmt.Errorf("reconciling CompositeCluster %s: %w", cc.Name, err)
	}

	fresh := &infrav1.CompositeCluster{}
	if getErr := k8s.Get(ctx(t), client.ObjectKeyFromObject(cc), fresh); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			return res, nil, err
		}

		require.NoError(t, getErr)
	}

	return res, fresh, err
}

// getUnstructured fetches any object by GVK, or nil when absent.
func getUnstructured(t *testing.T, gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	t.Helper()

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	err := k8s.Get(ctx(t), types.NamespacedName{Namespace: namespace, Name: name}, obj)
	if apierrors.IsNotFound(err) {
		return nil
	}

	require.NoError(t, err)

	return obj
}

// markFakeClusterReady simulates the provider: FakeCluster is a v1beta1
// contract CRD, so readiness is status.ready.
func markFakeClusterReady(t *testing.T, namespace, name string) {
	t.Helper()

	child := getUnstructured(t, fakeClusterGVK, namespace, name)
	require.NotNil(t, child, "the child must exist before it can be marked ready")
	require.NoError(t, unstructured.SetNestedField(child.Object, true, "status", "ready"))
	require.NoError(t, k8s.Status().Update(ctx(t), child))
}

// setEndpoint simulates the Kamaji control plane provider patching the
// endpoint onto the infrastructureRef target, and CAPI then copying it onto
// the Cluster. Both writes are spec writes on objects the tests own.
func setEndpoint(t *testing.T, cc *infrav1.CompositeCluster, cluster *clusterv1.Cluster, host string, port int32) {
	t.Helper()

	fresh := &infrav1.CompositeCluster{}
	require.NoError(t, k8s.Get(ctx(t), client.ObjectKeyFromObject(cc), fresh))
	fresh.Spec.ControlPlaneEndpoint = infrav1.APIEndpoint{Host: host, Port: port}
	require.NoError(t, k8s.Update(ctx(t), fresh))

	if cluster != nil {
		freshCluster := &clusterv1.Cluster{}
		require.NoError(t, k8s.Get(ctx(t), client.ObjectKeyFromObject(cluster), freshCluster))
		freshCluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: host, Port: port}
		require.NoError(t, k8s.Update(ctx(t), freshCluster))
	}
}

// condition returns the named condition, or nil.
func condition(conds []metav1.Condition, condType string) *metav1.Condition {
	return apimeta.FindStatusCondition(conds, condType)
}

// requireCondition asserts a condition's status and reason.
func requireCondition(t *testing.T, conds []metav1.Condition, condType string, status metav1.ConditionStatus, reason string) {
	t.Helper()

	c := condition(conds, condType)
	require.NotNil(t, c, "condition %s missing (have %v)", condType, conds)
	require.Equal(t, status, c.Status, "condition %s: %s", condType, c.Message)
	require.Equal(t, reason, c.Reason, "condition %s: %s", condType, c.Message)
}
