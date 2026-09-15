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

package capi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/csaupgrade"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// FieldOwner is the server-side-apply field manager for everything this
// controller writes.
const FieldOwner = client.FieldOwner("capi-composite")

var (
	// ErrChildNotOwned means an object of that name and kind exists but
	// something else controls it. Adopting it could hand another controller's
	// resources to a tenant, so we refuse and surface it instead.
	ErrChildNotOwned = errors.New("child exists but is not controlled by us")

	// ErrNoControllerOwner means ApplyChild was called without a controller
	// owner, which would produce an unwatched, un-collectable orphan.
	ErrNoControllerOwner = errors.New("controller owner is required")
)

// Ownership describes how a generated child is owned and labelled.
//
// Two ownerReferences, not one, and the split is load-bearing:
//
//   - Controller is us. controller-runtime's Owns() builder matches the
//     controller ownerRef, so without it we get no watches and fall back to
//     polling, and we cannot garbage-collect our own children.
//
//   - Cluster is a NON-controller ownerRef on the CAPI Cluster. Upstream
//     provider cluster controllers call util.GetOwnerCluster on their own
//     object to find the Cluster they belong to. That helper walks ALL
//     ownerReferences matching Kind == "Cluster" and does not require
//     controller: true. Omit it and CAPOSC/CAPO return early and provision
//     nothing -- with no error, no event and no condition. Only one ownerRef
//     may be the controller, so this is the only arrangement that satisfies
//     both mechanisms.
//
// See docs/design.md for the dual-ownership model.
type Ownership struct {
	// Controller is the object that controls the child (the CompositeCluster).
	Controller client.Object

	// Cluster is the owning CAPI Cluster, attached as a non-controller owner.
	Cluster *clusterv1.Cluster

	// ClusterAPIVersion is the Cluster apiVersion to stamp on that ownerRef —
	// the version the API server actually serves (resolved from the REST
	// mapper by the caller). CAPI's own controllers rewrite a Cluster
	// ownerRef whose version differs from theirs, and the garbage collector
	// cannot resolve an unserved version, so stamping a compiled-in version
	// is only safe when it happens to match the core. Empty falls back to the
	// version this module compiles against.
	ClusterAPIVersion string

	// Labels are merged onto the child.
	Labels map[string]string

	// Annotations are merged onto the child.
	Annotations map[string]string
}

// ApplyChild creates or updates a generated provider object.
//
// The name is chosen by the caller. Canonical InfraClusters have the same name
// as the owning CompositeCluster. Upstream machine controllers resolve their
// InfraCluster by NAME from Cluster.spec.infrastructureRef.Name without checking
// the kind, so same-name/different-kind children let two providers coexist under
// one CAPI Cluster. Additional regional children may use explicit bound names,
// while a canonical child remains available for each kind. Regional worker
// configuration belongs to the native provider; names do not authorize adoption.
//
// Writes go through Server-Side Apply rather than Update. Kamaji and the
// upstream providers write fields on these same objects -- notably
// spec.controlPlaneEndpoint and, on OpenStack, spec.apiServerFixedIP -- and a
// full Update would fight them on every reconcile.
func ApplyChild(
	ctx context.Context,
	c client.Client,
	gvk schema.GroupVersionKind,
	name, namespace string,
	spec map[string]any,
	own Ownership,
) (*unstructured.Unstructured, error) {
	if err := validateOwnerIdentity(own, namespace); err != nil {
		return nil, fmt.Errorf("ApplyChild: %s %q: %w", gvk.Kind, name, err)
	}

	refs, err := ownerRefs(own, c.Scheme())
	if err != nil {
		return nil, fmt.Errorf("ApplyChild: %s %q: %w", gvk.Kind, name, err)
	}

	obj := &unstructured.Unstructured{Object: map[string]any{}}
	obj.SetGroupVersionKind(gvk)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	obj.SetLabels(own.Labels)

	obj.SetAnnotations(own.Annotations)

	obj.SetOwnerReferences(refs)

	if err := unstructured.SetNestedMap(obj.Object, spec, "spec"); err != nil {
		return nil, fmt.Errorf("ApplyChild: %s %q: setting spec: %w", gvk.Kind, name, err)
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(gvk)
	err = c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, existing)
	if IsObjectNotFound(err, name) {
		// A create, unlike apply, cannot adopt a concurrently created object.
		if err = c.Create(ctx, obj, FieldOwner); err == nil {
			return obj, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("ApplyChild: creating %s %q: %w", gvk.Kind, name, err)
		}
		err = c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, existing)
	}
	if err != nil {
		return nil, fmt.Errorf("ApplyChild: getting %s %q: %w", gvk.Kind, name, err)
	}
	if err := prepareExistingChild(ctx, c, existing, own); err != nil {
		return nil, err
	}

	// Preserve the ownership decision across both replacement and same-UID races.
	obj.SetUID(existing.GetUID())
	obj.SetResourceVersion(existing.GetResourceVersion())
	if err := c.Apply(ctx, client.ApplyConfigurationFromUnstructured(obj), FieldOwner, client.ForceOwnership); err != nil {
		return nil, fmt.Errorf("ApplyChild: applying %s %q: %w", gvk.Kind, name, err)
	}

	return obj, nil
}

// PrepareChildForApply transfers our creation-time field ownership before a
// dry-run apply. It never creates children, adopts foreign objects or writes spec.
func PrepareChildForApply(ctx context.Context, c client.Client, desired *unstructured.Unstructured, own Ownership) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())
	if err := c.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
		if IsObjectNotFound(err, desired.GetName()) {
			return nil
		}
		return fmt.Errorf("preparing child apply: %w", err)
	}
	return prepareExistingChild(ctx, c, existing, own)
}

func prepareExistingChild(ctx context.Context, c client.Client, existing *unstructured.Unstructured, own Ownership) error {
	if err := CheckChildOwnership(existing, own, c.Scheme()); err != nil {
		return err
	}
	return migrateApplyOwnership(ctx, c, existing)
}

func migrateApplyOwnership(ctx context.Context, c client.Client, obj *unstructured.Unstructured) error {
	versions := sets.New[string]()
	for _, entry := range obj.GetManagedFields() {
		if entry.Manager == string(FieldOwner) && entry.Subresource == "" {
			versions.Insert(entry.APIVersion)
		}
	}
	if versions.Len() > 1 {
		return fmt.Errorf(
			"child %s %q has mixed-version field ownership requiring migration review: %w",
			obj.GetKind(),
			obj.GetName(),
			ErrFieldOwnership,
		)
	}
	updated := obj.DeepCopy()
	if err := csaupgrade.UpgradeManagedFields(updated, sets.New(string(FieldOwner)), string(FieldOwner)); err != nil {
		return fmt.Errorf("upgrading child field ownership: %w", err)
	}
	if reflect.DeepEqual(obj.GetManagedFields(), updated.GetManagedFields()) {
		return nil
	}
	// Only our Update entries are merged into Apply; other managers/subresources
	// remain unchanged. Both guards protect the ownership decision across races.
	data, err := json.Marshal(map[string]any{"apiVersion": obj.GetAPIVersion(), "kind": obj.GetKind(), "metadata": map[string]any{
		"uid": obj.GetUID(), "resourceVersion": obj.GetResourceVersion(), "managedFields": updated.GetManagedFields(),
	}})
	if err != nil {
		return fmt.Errorf("encoding field ownership migration: %w", err)
	}
	if err := c.Patch(ctx, obj, client.RawPatch(types.MergePatchType, data), FieldOwner); err != nil {
		return fmt.Errorf("persisting child field ownership: %w", err)
	}
	return nil
}

// ErrFieldOwnership requires an explicit review of incompatible persisted field sets.
var ErrFieldOwnership = errors.New("cannot safely migrate child field ownership")

// CheckChildOwnership rejects implicit adoption and conflicting Cluster owners.
// API versions may change during upgrades; identity is the group, kind, name and UID.
func CheckChildOwnership(obj client.Object, own Ownership, scheme *runtime.Scheme) error {
	if err := validateOwnerIdentity(own, obj.GetNamespace()); err != nil {
		return err
	}
	refs, err := ownerRefs(own, scheme)
	if err != nil {
		return fmt.Errorf("CheckChildOwnership: resolving expected owners: %w", err)
	}
	ref := metav1.GetControllerOf(obj)
	expected := refs[0]
	if ref == nil || !sameOwner(ref, &expected) {
		actual := "no controller owner"
		if ref != nil {
			actual = fmt.Sprintf("%s %q", ref.Kind, ref.Name)
		}
		return fmt.Errorf(
			"%s %q has %s instead of the expected controller: %w",
			obj.GetObjectKind().GroupVersionKind().Kind,
			obj.GetName(),
			actual,
			ErrChildNotOwned,
		)
	}
	if own.Cluster == nil {
		return nil
	}
	return checkClusterOwner(obj, own.Cluster)
}

func validateOwnerIdentity(own Ownership, namespace string) error {
	if own.Controller == nil || own.Controller.GetUID() == "" {
		return ErrNoControllerOwner
	}
	if own.Controller.GetNamespace() != namespace {
		return ErrChildNotOwned
	}
	if own.Cluster != nil {
		if own.Cluster.Namespace != namespace || own.Cluster.UID == "" {
			return ErrChildNotOwned
		}
	}
	return nil
}

func sameOwner(actual, expected *metav1.OwnerReference) bool {
	return actual.UID == expected.UID && actual.Name == expected.Name && actual.Kind == expected.Kind &&
		schema.FromAPIVersionAndKind(actual.APIVersion, actual.Kind).Group == schema.FromAPIVersionAndKind(expected.APIVersion, expected.Kind).Group
}

func checkClusterOwner(obj client.Object, cluster *clusterv1.Cluster) error {
	matched := false
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind != clusterv1.ClusterKind || schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind).Group != clusterv1.GroupVersion.Group {
			continue
		}
		if ref.UID != cluster.UID || ref.Name != cluster.Name {
			return fmt.Errorf(
				"%s %q has a conflicting Cluster owner: %w",
				obj.GetObjectKind().GroupVersionKind().Kind,
				obj.GetName(),
				ErrChildNotOwned,
			)
		}
		matched = true
	}
	if !matched {
		return fmt.Errorf("%s %q has no matching Cluster owner: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), ErrChildNotOwned)
	}
	return nil
}

// IsObjectNotFound distinguishes a named object's absence from an unavailable API route.
func IsObjectNotFound(err error, name string) bool {
	if name == "" || !apierrors.IsNotFound(err) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	details := status.Status().Details
	return details != nil && details.Name == name
}

// ownerRefs builds the two references. The controller's GVK is taken from
// its TypeMeta when set (cache-backed reads populate it) and resolved from
// the scheme otherwise (direct-client reads, as in envtest, leave it empty —
// and an ownerRef with an empty apiVersion is rejected by the API server).
func ownerRefs(own Ownership, scheme *runtime.Scheme) ([]metav1.OwnerReference, error) {
	refs := make([]metav1.OwnerReference, 0, 2)

	ctrlGVK := own.Controller.GetObjectKind().GroupVersionKind()
	if ctrlGVK.Empty() {
		var err error

		ctrlGVK, err = apiutil.GVKForObject(own.Controller, scheme)
		if err != nil {
			return nil, fmt.Errorf("ownerRefs: resolving the controller's kind: %w", err)
		}
	}

	refs = append(refs, metav1.OwnerReference{
		APIVersion:         ctrlGVK.GroupVersion().String(),
		Kind:               ctrlGVK.Kind,
		Name:               own.Controller.GetName(),
		UID:                own.Controller.GetUID(),
		Controller:         new(true),
		BlockOwnerDeletion: new(true),
	})

	if own.Cluster != nil {
		apiVersion := own.ClusterAPIVersion
		if apiVersion == "" {
			apiVersion = clusterv1.GroupVersion.String()
		}

		// Non-controller on purpose: see the Ownership doc comment.
		refs = append(refs, metav1.OwnerReference{
			APIVersion: apiVersion,
			Kind:       "Cluster",
			Name:       own.Cluster.Name,
			UID:        own.Cluster.UID,
		})
	}

	return refs, nil
}
