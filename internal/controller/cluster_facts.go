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

// Package controller holds the CompositeCluster reconciler. Rendering is
// delegated to internal/mint and contract plumbing to internal/capi; what
// lives here is orchestration: ordering, conditions, ownership and teardown.
package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
)

// clusterFacts is the subset of the owning CAPI Cluster the reconciler needs,
// read through unstructured paths.
//
// The Cluster is deliberately NOT read through typed clusterv1 structs: management
// clusters can serve different CAPI core versions, and a typed Get pins this binary
// to whichever version its module compiles against. It would fail at runtime if
// the core stopped serving that version. Every field below lives at the same path
// in v1beta1 and v1beta2, so the reader uses the served version resolved from the
// REST mapper.
type clusterFacts struct {
	Name      string
	Namespace string
	UID       types.UID

	// Paused is Cluster.spec.paused; the caller must still OR it with the
	// paused annotation on our own object.
	Paused bool

	// Endpoint is Cluster.spec.controlPlaneEndpoint — the fallback endpoint
	// source when the control plane provider has not patched ours.
	Endpoint infrav1.APIEndpoint

	// Deleting is set once the Cluster carries a deletionTimestamp. From then
	// on CAPI owns teardown (it deletes MachineDeployments and, last, the
	// infrastructure object); minting anything is fighting it.
	Deleting bool
}

// ClusterReader resolves and reads CAPI Clusters at whatever API version the
// server serves.
type ClusterReader struct {
	client client.Reader

	// gvk is the served Cluster GroupVersionKind, resolved once at setup from
	// the REST mapper.
	gvk schema.GroupVersionKind
}

// NewClusterReader builds a reader for the given served Cluster version.
func NewClusterReader(c client.Reader, gvk schema.GroupVersionKind) *ClusterReader {
	return &ClusterReader{client: c, gvk: gvk}
}

// APIVersion is the served Cluster apiVersion — what ownerRefs to the
// Cluster are stamped with, so they match what the core's own controllers
// write and what the garbage collector can resolve.
func (r *ClusterReader) APIVersion() string { return r.gvk.GroupVersion().String() }

// get fetches the named Cluster. A missing Cluster is (nil, nil): callers
// treat it as "waiting", not as an error.
func (r *ClusterReader) get(ctx context.Context, namespace, name string) (*clusterFacts, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r.gvk)

	err := r.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj)
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil //nolint:nilnil // absence is a wait state, not an error
	case err != nil:
		return nil, fmt.Errorf("clusterReader.get: %s/%s: %w", namespace, name, err)
	}

	facts := &clusterFacts{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		UID:       obj.GetUID(),
		Deleting:  obj.GetDeletionTimestamp() != nil,
	}

	facts.Paused, _, err = unstructured.NestedBool(obj.Object, "spec", "paused")
	if err != nil {
		return nil, fmt.Errorf("clusterReader.get: reading spec.paused: %w", err)
	}

	facts.Endpoint, err = capi.ReadEndpoint(obj)
	if err != nil {
		return nil, fmt.Errorf("clusterReader.get: reading endpoint: %w", err)
	}

	return facts, nil
}

// getOwner walks obj's ownerReferences for the owning CAPI Cluster — the same
// semantics as upstream util.GetOwnerCluster (any ownerRef with Kind "Cluster"
// in the CAPI core group, controller or not), without the typed client.
// (nil, nil) until CAPI has adopted the object.
func (r *ClusterReader) getOwner(ctx context.Context, obj metav1.Object) (*clusterFacts, error) {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind != clusterv1.ClusterKind {
			continue
		}

		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, fmt.Errorf("clusterReader.getOwner: parsing ownerRef apiVersion %q: %w", ref.APIVersion, err)
		}

		if gv.Group == clusterv1.GroupVersion.Group {
			facts, err := r.get(ctx, obj.GetNamespace(), ref.Name)
			if facts != nil && facts.UID != ref.UID {
				return nil, nil //nolint:nilnil // a reused name does not authorize the replacement Cluster
			}
			return facts, err
		}
	}

	return nil, nil //nolint:nilnil // not adopted yet is a wait state
}

// isPaused reports whether reconciliation is paused, from either the owning
// Cluster's spec.paused or the paused annotation on our own object. The
// Cluster may be nil (not adopted yet).
func isPaused(cluster *clusterFacts, obj metav1.Object) bool {
	if cluster != nil && cluster.Paused {
		return true
	}

	_, ok := obj.GetAnnotations()[clusterv1.PausedAnnotation]

	return ok
}
