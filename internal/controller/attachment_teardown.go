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
	"maps"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
)

func (r *CompositeClusterReconciler) retireAttachments(ctx context.Context, cc *infrav1.CompositeCluster, plan *attachmentPlan) (bool, error) {
	pending := false
	for _, name := range slices.Sorted(maps.Keys(plan.retiring)) {
		waiting, err := r.retireAttachment(ctx, cc, name, plan)
		if err != nil {
			return false, err
		}
		pending = pending || waiting
	}
	return pending, nil
}

func (r *CompositeClusterReconciler) retireAttachment(
	ctx context.Context,
	cc *infrav1.CompositeCluster,
	name string,
	plan *attachmentPlan,
) (bool, error) {
	p := plan.retiring[name]
	entry := plan.bindings.Providers[name]
	if pending, err := retirementBlocked(cc, p, entry); pending || err != nil {
		return pending, err
	}
	if entry == nil {
		return false, nil
	}
	if entry.Retired {
		return false, r.releaseAttachment(ctx, cc, name, entry)
	}
	busy, err := r.attachmentHasConsumers(ctx, cc, entry.Binding.BootstrapName)
	if err != nil {
		return false, err
	}
	if busy || canonicalAttachmentRequired(cc, name, plan.bindings) {
		return true, r.reportAttachmentDeleting(ctx, p)
	}
	return r.completeAttachmentRetirement(ctx, cc, name, plan)
}

func retirementBlocked(cc *infrav1.CompositeCluster, p *infrav1.CompositeProvider, entry *attachmentBinding) (bool, error) {
	if p == nil {
		return false, nil
	}
	if isPaused(nil, p) {
		return true, nil
	}
	if entry != nil && (entry.UID != p.UID || entry.ClusterUID != cc.UID) {
		return false, errProfileMismatch
	}
	if entry == nil && p.Annotations[infrav1.ProviderBindingAnnotation] != "" {
		return false, errProfilePinMissing
	}
	return false, nil
}

func (r *CompositeClusterReconciler) reportAttachmentDeleting(ctx context.Context, p *infrav1.CompositeProvider) error {
	if p == nil {
		return nil
	}
	return r.reportAttachmentFailure(
		ctx,
		p,
		infrav1.DeletingReason,
		"Waiting for native worker consumers or dependent regional attachments to disappear.",
	)
}

func (r *CompositeClusterReconciler) completeAttachmentRetirement(
	ctx context.Context,
	cc *infrav1.CompositeCluster,
	name string,
	plan *attachmentPlan,
) (bool, error) {
	entry := plan.bindings.Providers[name]
	gone, err := r.deleteAttachmentInfrastructure(ctx, cc, entry)
	if err != nil || !gone {
		return !gone, err
	}
	gone, err = r.childGone(ctx, cc, kubeadmConfigTemplateGVK, entry.Binding.BootstrapName)
	if err != nil || !gone {
		return !gone, err
	}
	entry.Retired = true
	if err := r.persistAttachmentRetirement(ctx, cc, plan.bindings); err != nil {
		return false, err
	}
	return false, r.releaseAttachment(ctx, cc, name, entry)
}

func canonicalAttachmentRequired(cc *infrav1.CompositeCluster, name string, bindings attachmentBindings) bool {
	current := bindings.Providers[name]
	if current.Binding.ChildName != cc.Name {
		return false
	}
	for key, other := range bindings.Providers {
		if key != name && !other.Retired && other.Binding.groupKind() == current.Binding.groupKind() {
			return true
		}
	}
	return false
}

func (r *CompositeClusterReconciler) deleteAttachmentInfrastructure(
	ctx context.Context,
	cc *infrav1.CompositeCluster,
	entry *attachmentBinding,
) (bool, error) {
	contract, err := r.Contracts.Get(ctx, entry.Binding.groupKind())
	if err != nil {
		return false, fmt.Errorf("resolving attached infrastructure for deletion: %w", err)
	}
	return r.childGone(ctx, cc, schema.FromAPIVersionAndKind(contract.APIVersion, entry.Binding.Kind), entry.Binding.ChildName)
}

func (r *CompositeClusterReconciler) persistAttachmentRetirement(
	ctx context.Context,
	cc *infrav1.CompositeCluster,
	bindings attachmentBindings,
) error {
	raw, err := encodeAttachmentBindings(cc, bindings)
	if err != nil {
		return err
	}
	if cc.Annotations[infrav1.ProviderAttachmentsAnnotation] == raw {
		return nil
	}
	cc.Annotations[infrav1.ProviderAttachmentsAnnotation] = raw
	if err := r.Update(ctx, cc); err != nil {
		return fmt.Errorf("persisting completed provider teardown: %w", err)
	}
	return nil
}

func (r *CompositeClusterReconciler) releaseAttachment(
	ctx context.Context,
	cc *infrav1.CompositeCluster,
	name string,
	binding *attachmentBinding,
) error {
	p := &infrav1.CompositeProvider{}
	key := client.ObjectKey{Namespace: cc.Namespace, Name: name}
	if err := r.apiReader().Get(ctx, key, p); err != nil {
		if capi.IsObjectNotFound(err, name) {
			return nil
		}
		return fmt.Errorf("observing drained provider: %w", err)
	}
	if p.UID != binding.UID || p.Spec.ClusterRef.Name != cc.Name {
		return errProfileMismatch
	}
	if !cc.DeletionTimestamp.IsZero() {
		gone, err := r.deleteDrainedProvider(ctx, p)
		if err != nil || gone {
			return err
		}
	}
	if controllerutil.RemoveFinalizer(p, infrav1.ProviderAttachmentFinalizer) {
		if err := r.Update(ctx, p); err != nil {
			return fmt.Errorf("releasing drained provider: %w", err)
		}
	}
	return nil
}

func (r *CompositeClusterReconciler) deleteDrainedProvider(ctx context.Context, p *infrav1.CompositeProvider) (bool, error) {
	if !p.DeletionTimestamp.IsZero() {
		return false, nil
	}
	uid, version := p.UID, p.ResourceVersion
	if err := r.Delete(ctx, p, client.Preconditions{UID: &uid, ResourceVersion: &version}); err != nil {
		return false, fmt.Errorf("deleting drained provider: %w", err)
	}
	if err := r.apiReader().Get(ctx, client.ObjectKeyFromObject(p), p); err != nil {
		if capi.IsObjectNotFound(err, p.Name) {
			return true, nil
		}
		return false, fmt.Errorf("refreshing drained provider: %w", err)
	}
	return false, nil
}

// Worker resources are observed, never managed. Unknown bootstrap provenance
// retains infrastructure rather than guessing that an orphaned Machine is unrelated.
func (r *CompositeClusterReconciler) attachmentHasConsumers(ctx context.Context, cc *infrav1.CompositeCluster, bootstrapName string) (bool, error) {
	own := compositeOwnership(cc)
	if own.Cluster == nil {
		return true, nil
	}
	for _, kind := range []string{"MachineDeployment", "MachineSet", "Machine"} {
		uses, err := r.workerKindUsesBootstrap(ctx, cc.Namespace, own.Cluster.Name, kind, bootstrapName)
		if err != nil || uses {
			return uses, err
		}
	}
	return false, nil
}

func (r *CompositeClusterReconciler) workerKindUsesBootstrap(ctx context.Context, namespace, clusterName, kind, bootstrapName string) (bool, error) {
	continuation := ""
	for {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(clusterv1.GroupVersion.WithKind(kind + "List"))
		if err := r.apiReader().List(ctx, list, client.InNamespace(namespace), client.Limit(100), client.Continue(continuation)); err != nil {
			return false, fmt.Errorf("observing worker consumers: %w", err)
		}
		for i := range list.Items {
			uses, err := r.workerUsesBootstrap(ctx, &list.Items[i], clusterName, kind, bootstrapName)
			if err != nil || uses {
				return uses, err
			}
		}
		if list.GetContinue() == "" {
			return false, nil
		}
		continuation = list.GetContinue()
	}
}

func (r *CompositeClusterReconciler) workerUsesBootstrap(
	ctx context.Context,
	obj *unstructured.Unstructured,
	clusterName, kind, bootstrapName string,
) (bool, error) {
	ownerName, _, err := unstructured.NestedString(obj.Object, "spec", "clusterName")
	if err != nil {
		return false, fmt.Errorf("reading worker cluster reference: %w", err)
	}
	if ownerName != clusterName {
		return false, nil
	}
	if kind == "Machine" {
		return r.machineUsesBootstrap(ctx, obj, bootstrapName)
	}
	name, _, err := unstructured.NestedString(obj.Object, "spec", "template", "spec", "bootstrap", "configRef", "name")
	if err != nil {
		return false, fmt.Errorf("reading worker bootstrap reference: %w", err)
	}
	return name == bootstrapName, nil
}

func (r *CompositeClusterReconciler) machineUsesBootstrap(
	ctx context.Context,
	machine *unstructured.Unstructured,
	bootstrapName string,
) (bool, error) {
	ref, found, err := unstructured.NestedStringMap(machine.Object, "spec", "bootstrap", "configRef")
	if err != nil {
		return false, fmt.Errorf("reading Machine bootstrap identity: %w", err)
	}
	if !found {
		return false, nil // Externally supplied bootstrap data does not consume a generated template.
	}
	gvk, err := r.RESTMapper().RESTMapping(schema.GroupKind{Group: ref["apiGroup"], Kind: ref["kind"]})
	if err != nil {
		return false, fmt.Errorf("resolving Machine bootstrap API: %w", err)
	}
	metadata := &metav1.PartialObjectMetadata{}
	metadata.SetGroupVersionKind(gvk.GroupVersionKind)
	if err := r.apiReader().Get(ctx, client.ObjectKey{Namespace: machine.GetNamespace(), Name: ref["name"]}, metadata); err != nil {
		if capi.IsObjectNotFound(err, ref["name"]) {
			return true, nil
		}
		return false, fmt.Errorf("observing Machine bootstrap provenance: %w", err)
	}
	source := metadata.Annotations["cluster.x-k8s.io/cloned-from-name"]
	return source == "" || source == bootstrapName, nil
}

func (r *CompositeClusterReconciler) deleteAttachedInfrastructure(
	ctx context.Context,
	cc *infrav1.CompositeCluster,
	attachments attachmentBindings,
) (bool, error) {
	remaining := false
	for _, entry := range attachments.Providers {
		if entry.Retired {
			continue
		}
		busy, err := r.attachmentHasConsumers(ctx, cc, entry.Binding.BootstrapName)
		if err != nil {
			return false, err
		}
		if busy {
			remaining = true
			continue
		}
		gone, err := r.deleteAttachmentInfrastructure(ctx, cc, entry)
		if err != nil {
			return false, err
		}
		remaining = remaining || !gone
	}
	return remaining, nil
}

func (r *CompositeClusterReconciler) deleteAttachedBootstrap(
	ctx context.Context,
	cc *infrav1.CompositeCluster,
	attachments attachmentBindings,
) (bool, error) {
	remaining := false
	for name, entry := range attachments.Providers {
		if !entry.Retired {
			gone, err := r.childGone(ctx, cc, kubeadmConfigTemplateGVK, entry.Binding.BootstrapName)
			if err != nil {
				return false, err
			}
			if !gone {
				remaining = true
				continue
			}
			entry.Retired = true
			if err := r.persistAttachmentRetirement(ctx, cc, attachments); err != nil {
				return false, err
			}
		}
		if err := r.releaseAttachment(ctx, cc, name, entry); err != nil {
			return false, err
		}
	}
	return remaining, nil
}
