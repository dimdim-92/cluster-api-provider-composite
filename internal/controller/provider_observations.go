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
	"slices"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

const maxProviderNodePools = 100

var errNodePoolStatusCapacity = errors.New("node-pool status reference capacity exceeded")

func (r *CompositeClusterReconciler) refreshProviderStatuses(ctx context.Context, cc *infrav1.CompositeCluster, cluster *clusterFacts) error {
	if cluster == nil {
		markProviderObservationsUnknown(cc, infrav1.WaitingForClusterReason, "Waiting for a verified owning Cluster observation.")
		return nil
	}
	if err := r.observeProviderStatuses(ctx, cc, cluster); err != nil {
		// This inventory is informational, not an infrastructure readiness gate.
		markProviderObservationsUnknown(cc, infrav1.WaitingForProvidersReason, diagnosticMessage(err))
		return err
	}
	return nil
}

// Publish only a complete direct-read inventory. Neither these observations nor
// worker labels authorize adoption, binding reconstruction or worker mutations.
func (r *CompositeClusterReconciler) observeProviderStatuses(ctx context.Context, cc *infrav1.CompositeCluster, cluster *clusterFacts) error {
	providers, err := r.attachmentProviders(ctx, cc)
	if err != nil {
		return err
	}
	bindings, err := readAttachmentBindings(cc)
	if err != nil {
		return err
	}
	statuses, bootstrapOwners, err := projectProviderStatuses(cc, providers, bindings, cluster.Deleting || !cc.DeletionTimestamp.IsZero())
	if err != nil {
		return err
	}
	if len(bootstrapOwners) != 0 {
		if err := r.observeNodePools(ctx, cc.Namespace, cluster.Name, statuses, bootstrapOwners); err != nil {
			return err
		}
	}
	cc.Status.Providers = statuses
	cc.Status.ProviderNames = providerNames(statuses)
	return nil
}

func projectProviderStatuses(
	cc *infrav1.CompositeCluster, providers []infrav1.CompositeProvider, bindings attachmentBindings, deleting bool,
) ([]infrav1.ProviderStatus, map[string]int, error) {
	previous := map[string]*infrav1.ProviderStatus{}
	for i := range cc.Status.Providers {
		p := &cc.Status.Providers[i]
		previous[string(p.Name)] = p
	}
	byName := map[string]*infrav1.ProviderStatus{}
	poolBootstrap := map[string]string{}
	for i := range providers {
		p := &providers[i]
		byName[p.Name], poolBootstrap[p.Name] = boundAttachmentStatus(p, previous[p.Name], bindings.Providers[p.Name], deleting)
	}
	// A provider absent before retirement completes must remain visible while
	// durable identity still protects its workers and children.
	for name, binding := range bindings.Providers {
		if _, present := byName[name]; present || binding.Retired {
			continue
		}
		byName[name] = retiringProviderStatus(cc, name, binding, previous[name])
		poolBootstrap[name] = binding.Binding.BootstrapName
	}
	if len(byName) > maxProviderBindings {
		return nil, nil, errInvalidComposition
	}
	statuses := make([]infrav1.ProviderStatus, 0, len(byName))
	for _, ps := range byName {
		statuses = append(statuses, *ps)
	}
	slices.SortFunc(statuses, func(a, b infrav1.ProviderStatus) int { return strings.Compare(string(a.Name), string(b.Name)) })
	bootstrapOwners := map[string]int{}
	for i := range statuses {
		if bootstrap := poolBootstrap[string(statuses[i].Name)]; bootstrap != "" {
			bootstrapOwners[bootstrap] = i
		}
	}
	return statuses, bootstrapOwners, nil
}

func boundAttachmentStatus(
	p *infrav1.CompositeProvider, previous *infrav1.ProviderStatus, binding *attachmentBinding, deleting bool,
) (status *infrav1.ProviderStatus, bootstrapName string) {
	ps := attachmentStatus(p, previous, deleting)
	if binding == nil || binding.UID != p.UID || binding.Retired {
		return ps, ""
	}
	ps.Profile = binding.Binding.profileStatus()
	ps.Provisioned = p.Status.DeepCopy().Initialization.Provisioned
	if p.Status.InfrastructureRef.Name != "" {
		ref := p.Status.InfrastructureRef
		ps.ChildRef = &ref
	}
	if p.Status.BootstrapTemplateRef.Name != "" {
		ref := p.Status.BootstrapTemplateRef
		ps.BootstrapTemplateRef = &ref
	}
	return ps, binding.Binding.BootstrapName
}

func retiringProviderStatus(
	cc *infrav1.CompositeCluster, name string, binding *attachmentBinding, previous *infrav1.ProviderStatus,
) *infrav1.ProviderStatus {
	ps := &infrav1.ProviderStatus{Name: infrav1.ProviderName(name), Profile: binding.Binding.profileStatus()}
	if previous != nil {
		ps = previous.DeepCopy()
		ps.NodePools = nil
	}
	setProviderDeleting(ps, providerObservationGeneration(ps, cc.Generation))
	return ps
}

func attachmentStatus(p *infrav1.CompositeProvider, previous *infrav1.ProviderStatus, deleting bool) *infrav1.ProviderStatus {
	ps := &infrav1.ProviderStatus{Name: infrav1.ProviderName(p.Name), Conditions: slices.Clone(p.Status.Conditions)}
	ready := apimeta.FindStatusCondition(ps.Conditions, infrav1.ReadyCondition)
	if ready == nil || ready.ObservedGeneration != p.Generation {
		setCondition(&ps.Conditions, p.Generation, infrav1.ReadyCondition, metav1.ConditionUnknown,
			infrav1.WaitingForProvidersReason, "Waiting for a current provider observation.")
	}
	if deleting || !p.DeletionTimestamp.IsZero() {
		setProviderDeleting(ps, p.Generation)
	}
	ps.Conditions = mergeProviderConditions(ps.Conditions, previous)
	ready = apimeta.FindStatusCondition(ps.Conditions, infrav1.ReadyCondition)
	if ready.Status != metav1.ConditionTrue {
		ps.Reason, ps.Message = ready.Reason, clip(ready.Message)
	}
	return ps
}

// Summary conditions own their transition times: recovering from a failed
// inventory read must not copy an older provider timestamp back into status.
func mergeProviderConditions(current []metav1.Condition, previous *infrav1.ProviderStatus) []metav1.Condition {
	result := make([]metav1.Condition, 0, len(current))
	for _, condition := range current {
		if previous != nil {
			if old := apimeta.FindStatusCondition(previous.Conditions, condition.Type); old != nil {
				result = append(result, *old)
			}
		}
		condition.LastTransitionTime = metav1.Time{}
		apimeta.SetStatusCondition(&result, condition)
	}
	return result
}

func setProviderDeleting(ps *infrav1.ProviderStatus, generation int64) {
	message := "Waiting for provider or cluster retirement to complete."
	setFalse(&ps.Conditions, generation, infrav1.ReadyCondition, infrav1.DeletingReason, message)
	setCondition(&ps.Conditions, generation, infrav1.DeletingCondition, metav1.ConditionTrue, infrav1.DeletingReason, "")
	ps.Reason, ps.Message = infrav1.DeletingReason, message
}

func (r *CompositeClusterReconciler) nodePoolGVK() (schema.GroupVersionKind, error) {
	mapping, err := r.RESTMapper().RESTMapping(schema.GroupKind{Group: clusterv1.GroupVersion.Group, Kind: "MachineDeployment"})
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("resolving node-pool API: %w", err)
	}
	return mapping.GroupVersionKind, nil
}

func (r *CompositeClusterReconciler) observeNodePools(
	ctx context.Context, namespace, clusterName string, statuses []infrav1.ProviderStatus, bootstrapOwners map[string]int,
) error {
	gvk, err := r.nodePoolGVK()
	if err != nil {
		return err
	}
	continuation := ""
	for {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := r.apiReader().List(ctx, list, client.InNamespace(namespace), client.Limit(100), client.Continue(continuation)); err != nil {
			return fmt.Errorf("observing native node pools: %w", err)
		}
		for i := range list.Items {
			pool := &list.Items[i]
			bootstrap, err := nodePoolBootstrap(pool, clusterName)
			if err != nil {
				return err
			}
			if index, found := bootstrapOwners[bootstrap]; found {
				if len(statuses[index].NodePools) == maxProviderNodePools {
					return errNodePoolStatusCapacity
				}
				statuses[index].NodePools = append(statuses[index].NodePools, infrav1.ChildReference{
					APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind, Name: pool.GetName(),
				})
			}
		}
		if list.GetContinue() == "" {
			break
		}
		continuation = list.GetContinue()
	}
	for i := range statuses {
		slices.SortFunc(statuses[i].NodePools, func(a, b infrav1.ChildReference) int { return strings.Compare(a.Name, b.Name) })
	}
	return nil
}

func nodePoolBootstrap(pool *unstructured.Unstructured, clusterName string) (string, error) {
	ownerName, _, err := unstructured.NestedString(pool.Object, "spec", "clusterName")
	if err != nil {
		return "", fmt.Errorf("reading node-pool cluster reference: %w", err)
	}
	if ownerName != clusterName {
		return "", nil
	}
	ref, _, err := unstructured.NestedStringMap(pool.Object, "spec", "template", "spec", "bootstrap", "configRef")
	if err != nil {
		return "", fmt.Errorf("reading node-pool bootstrap reference: %w", err)
	}
	group := ref["apiGroup"]
	if group == "" && ref["apiVersion"] != "" {
		gv, err := schema.ParseGroupVersion(ref["apiVersion"])
		if err != nil {
			return "", fmt.Errorf("reading node-pool bootstrap API version: %w", err)
		}
		group = gv.Group
	}
	if group != kubeadmConfigTemplateGVK.Group || ref["kind"] != kubeadmConfigTemplateGVK.Kind {
		return "", nil
	}
	return ref["name"], nil
}

// Worker events refresh the read-only inventory and speed up drain observation.
// Mapping follows the actual Cluster reference, never an untrusted worker label.
func (r *CompositeClusterReconciler) nodePoolToComposite(ctx context.Context, obj client.Object) []reconcile.Request {
	pool, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	name, _, err := unstructured.NestedString(pool.Object, "spec", "clusterName")
	if err != nil || name == "" {
		return nil
	}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(r.Clusters.gvk)
	if err := r.apiReader().Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: name}, cluster); err != nil {
		ctrl.LoggerFrom(ctx).V(1).Info("Unable to map node pool to CompositeCluster; polling will retry", "diagnostic", diagnosticMessage(err))
		return nil
	}
	return r.clusterToComposite(ctx, cluster)
}

func providerObservationGeneration(p *infrav1.ProviderStatus, fallback int64) int64 {
	if ready := apimeta.FindStatusCondition(p.Conditions, infrav1.ReadyCondition); ready != nil {
		return ready.ObservedGeneration
	}
	return fallback
}

func markProviderObservationsUnknown(cc *infrav1.CompositeCluster, reason, message string) {
	for i := range cc.Status.Providers {
		p := &cc.Status.Providers[i]
		setCondition(&p.Conditions, providerObservationGeneration(p, cc.Generation), infrav1.ReadyCondition, metav1.ConditionUnknown, reason, message)
		p.Reason, p.Message = reason, clip(message)
	}
}
