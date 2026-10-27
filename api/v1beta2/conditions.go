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

package v1beta2

// Condition types and reasons.
//
// These are metav1.Condition values written through apimeta.SetStatusCondition.
const (
	// ReadyCondition is the summary condition, and the one CAPI surfaces.
	ReadyCondition = "Ready"

	// PausedCondition reports that reconciliation is paused, either through
	// Cluster.spec.paused or the cluster.x-k8s.io/paused annotation.
	PausedCondition = "Paused"

	// DeletingCondition reports that ordered teardown is in progress.
	DeletingCondition = "Deleting"

	// ProvidersReadyCondition reports that every child InfraCluster is
	// provisioned. CompositeCluster only.
	ProvidersReadyCondition = "ProvidersReady"

	// EndpointPropagatedCondition reports that the control plane endpoint has
	// been fanned out to every child InfraCluster. CompositeCluster only.
	EndpointPropagatedCondition = "EndpointPropagated"

	// BootstrapTemplatesReadyCondition reports that the per-provider
	// KubeadmConfigTemplates are minted from the bootstrap cascade at the
	// current payloads. CompositeCluster only, and only after
	// EndpointPropagated.
	BootstrapTemplatesReadyCondition = "BootstrapTemplatesReady"

	// ConfigurationValidCondition reports validation of profile defaults and policy.
	ConfigurationValidCondition = "ConfigurationValid"
)

// Condition reasons.
const (
	// ReadyReason is the healthy reason for any condition above.
	ReadyReason = "Ready"

	// PausedReason accompanies PausedCondition=True.
	PausedReason = "Paused"

	// NotPausedReason accompanies PausedCondition=False.
	NotPausedReason = "NotPaused"

	// DeletingReason accompanies DeletingCondition=True.
	DeletingReason = "Deleting"

	// NotDeletingReason accompanies DeletingCondition=False.
	NotDeletingReason = "NotDeleting"

	// ProviderNotInstalledReason means the provider's child CRDs are not
	// present on this Service Cluster. One image is deployable to both
	// single-cloud and multi-cloud Service Clusters, so this is an expected
	// state rather than a bug.
	ProviderNotInstalledReason = "ProviderNotInstalled"

	// RenderFailedReason means a child could not be rendered from its payload
	// (malformed payload, merge failure).
	RenderFailedReason = "RenderFailed"

	// SchemaValidationFailedReason means the rendered child was rejected by
	// the target CRD's schema during the server-side dry-run that precedes
	// every apply. The payload does not fit the provider's API.
	SchemaValidationFailedReason = "SchemaValidationFailed"

	// ChildNotOwnedReason means an object of the expected name and kind already
	// exists but is not controlled by us. We refuse to adopt it.
	ChildNotOwnedReason = "ChildNotOwned"

	// InvalidCompositionReason means provider identity or composition is contradictory.
	InvalidCompositionReason = "InvalidComposition"

	// WaitingForClusterReason means the owning CAPI Cluster is not resolvable
	// yet. Not an error: the Cluster watch requeues us.
	WaitingForClusterReason = "WaitingForCluster"

	// WaitingForEndpointReason means no control plane endpoint is available yet,
	// from either the control plane provider or the Cluster.
	WaitingForEndpointReason = "WaitingForEndpoint"

	// WaitingForProvidersReason means at least one child InfraCluster is not
	// provisioned.
	WaitingForProvidersReason = "WaitingForProviders"

	// ProfileUnavailableReason means a referenced profile cannot be used yet.
	ProfileUnavailableReason = "ProfileUnavailable"

	// ProfileMismatchReason means profile identity no longer matches the durable pin.
	ProfileMismatchReason = "ProfileMismatch"

	// OverrideRejectedReason means consumer input violates the profile policy.
	OverrideRejectedReason = "OverrideRejected"
)
