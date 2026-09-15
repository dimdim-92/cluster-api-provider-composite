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

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProviderSpec carries resolved attachment configuration for rendering.
// It is not part of CompositeClusterSpec or the cluster resource schema.
type ProviderSpec struct {
	// name is the stable CompositeProvider attachment key.
	// +required
	Name ProviderName `json:"name,omitempty"`

	// providerRef selects a cluster-scoped immutable profile. When set, region,
	// zones and endpointPaths come exclusively from that profile. infraSpec and
	// bootstrap are optional overrides subject to the profile's policy.
	// +optional
	ProviderRef CompositeProviderReference `json:"providerRef,omitempty,omitzero"`

	// infraClusterName opts a profile-backed attachment into a distinct native
	// InfraCluster name in this CompositeCluster's namespace. Omission uses the
	// CompositeCluster name. Each GroupKind must retain one such canonical child
	// for native worker lookup; additional regional worker settings remain native.
	// This field is immutable on the attachment. Names must be unique per GroupKind.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	InfraClusterName string `json:"infraClusterName,omitempty"`

	// region is resolved from the template to publish native failure-domain attributes.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Region string `json:"region,omitempty"`

	// zones are this provider's failure-domain names, exactly as the
	// provider expects them on a Machine's spec.failureDomain (an Outscale
	// subregion such as "eu-west-2a", an OpenStack availability zone such as
	// "nova"). They are republished verbatim in status.failureDomains, with
	// attributes naming the provider and region. Empty publishes none.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=256
	// +listType=set
	// +kubebuilder:validation:MinItems=0
	Zones []string `json:"zones,omitempty"`

	// infraSpec carries a native spec or JSON merge patch over template defaults. Overrides
	// must satisfy the allowed/protected policy. Native schema validation uses
	// server-side dry-run before every apply.
	// +optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	InfraSpec apiextensionsv1.JSON `json:"infraSpec,omitempty,omitzero"`

	// bootstrap is this provider's overlay on spec.bootstrap, merged with
	// RFC 7386 semantics (objects merge, arrays replace, null deletes) plus
	// one extension: name-keyed lists such as kubeletExtraArgs merge by name.
	// With providerRef, profile defaults are applied between shared bootstrap
	// and this overlay; the final result must satisfy the profile policy.
	// In practice it carries the kubelet provider-id entry, whose format must
	// match this provider's Machine.spec.providerID.
	// +optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Bootstrap *apiextensionsv1.JSON `json:"bootstrap,omitempty"`

	// endpointPaths are the JSON paths in the child spec that the endpoint
	// fan-out writes. A path ending in an endpoint-shaped field receives the
	// object form {host, port}; a path naming a recognized scalar suffix
	// receives the matching scalar (…FixedIP the host, …Port the port).
	// Defaults to ["spec.controlPlaneEndpoint"].
	// +optional
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=512
	// +kubebuilder:validation:items:Pattern=`^spec\.[a-zA-Z0-9_]+(\.[a-zA-Z0-9_]+)*$`
	// +listType=atomic
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:MinItems=0
	EndpointPaths []string `json:"endpointPaths,omitempty"`
}

// CompositeClusterSpec defines the desired state of a CompositeCluster.
// +kubebuilder:validation:MinProperties=0
type CompositeClusterSpec struct {
	// controlPlaneEndpoint is part of the CAPI InfraCluster contract.
	//
	// It is written by the Kamaji control plane provider, which patches exactly
	// ONE object: whichever is named in Cluster.spec.infrastructureRef. A
	// cluster spanning clouds has several InfraClusters, all of which need the
	// endpoint, so we fan it out to every child through each provider's
	// endpointPaths. That fan-out is the load-bearing reason this is a
	// delegator at all.
	// +optional
	ControlPlaneEndpoint APIEndpoint `json:"controlPlaneEndpoint,omitempty,omitzero"`

	// bootstrap is the ONE shared worker bootstrap payload — a
	// bootstrap.cluster.x-k8s.io/v1beta2 KubeadmConfigTemplate
	// spec.template.spec (kubeletExtraArgs as a list of {name, value}),
	// authored upstream: files, preKubeadmCommands, node labels, everything
	// provider-independent.
	//
	// For each provider it is merged with that provider's bootstrap overlay
	// (RFC 7386) and minted as a stable KubeadmConfigTemplate named
	// "<cluster>-<provider>", which that provider's MachineDeployments
	// reference. Minting happens only after EndpointPropagated: the kubeadm
	// bootstrap provider renders join commands from the settled endpoint, and
	// Cluster.spec.controlPlaneEndpoint is copy-once. The minted templates are
	// updated in place on payload changes and affect new machines only.
	// +optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Bootstrap *apiextensionsv1.JSON `json:"bootstrap,omitempty"`
}

// InitializationStatus satisfies the v1beta2 InfraCluster contract. The v1beta1
// contract used status.ready instead; both providers we generate children for
// are still on v1beta1 while we serve v1beta2, so the contract version is
// resolved per object from the installed CRD's contract labels.
// +kubebuilder:validation:MinProperties=1
type InitializationStatus struct {
	// provisioned latches completion of cluster infrastructure initialization.
	// Provider attachments report their own initialization independently.
	//
	// Kamaji does not set this for dynamically registered infrastructure kinds
	// -- it patches the endpoint and leaves readiness alone -- so it is ours.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// ProviderStatus summarizes one CompositeProvider attachment and its native node pools.
type ProviderStatus struct {
	// conditions mirror attachment health independently of cluster readiness.
	// Their observed generations refer to the CompositeProvider, not the cluster.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// name identifies the CompositeProvider attachment in this namespace.
	// +required
	Name ProviderName `json:"name,omitempty"`

	// profile reflects the durable profile pin and resolved regional identity.
	// It is absent before binding and is not binding authority.
	// +optional
	Profile ProviderProfileStatus `json:"profile,omitempty,omitzero"`

	// childRef is the generated InfraCluster.
	// +optional
	ChildRef *ChildReference `json:"childRef,omitempty,omitzero"`

	// bootstrapTemplateRef is the minted per-provider KubeadmConfigTemplate
	// ("<cluster>-<provider>") that this provider's MachineDeployments
	// reference. Set only after EndpointPropagated.
	// +optional
	BootstrapTemplateRef *ChildReference `json:"bootstrapTemplateRef,omitempty,omitzero"`

	// nodePools lists native MachineDeployments referencing this attachment's
	// generated bootstrap template and the owning CAPI Cluster. These are
	// observations only: Composite never manages workers or gates Ready on them.
	// At most 100 references fit in a complete status snapshot; larger or
	// unreadable inventories retain the last snapshot with Ready=Unknown.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=100
	NodePools []ChildReference `json:"nodePools,omitempty"`

	// provisioned reports the attachment's latched initialization, independently of ongoing health.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`

	// reason is the cloud-neutral taxonomy reason for this provider. It never
	// carries a provider-specific term.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=0
	Reason string `json:"reason,omitempty"`

	// message gives bounded diagnostic guidance and child context. Raw provider
	// and admission prose is withheld because it can contain credentials or
	// payload values; inspect the referenced child and schema with appropriate access.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:MinLength=0
	Message string `json:"message,omitempty"`

	// outputs is reserved for resolved provider facts. The controller does not
	// currently populate it; consumers must not depend on it being present.
	// +optional
	// +kubebuilder:validation:MaxProperties=32
	// +kubebuilder:validation:MinProperties=0
	Outputs map[string]string `json:"outputs,omitempty"`
}

// CompositeClusterStatus defines the observed state of a CompositeCluster.
// +kubebuilder:validation:MinProperties=0
type CompositeClusterStatus struct {
	// conditions describe current reconciliation and infrastructure health.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:MinItems=0
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// initialization records completion of the initial infrastructure provisioning.
	// +optional
	Initialization InitializationStatus `json:"initialization,omitempty,omitzero"`

	// failureDomains contains native provider zone names with provider and region
	// attributes. Conflicting names are rejected, not renamed or silently merged.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:MinItems=0
	FailureDomains []FailureDomain `json:"failureDomains,omitempty"`

	// providers summarizes current and draining attachments and their native
	// node pools, keyed by CompositeProvider name. Completed retirements leave
	// durable binding history, not live provider status entries.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:MinItems=0
	Providers []ProviderStatus `json:"providers,omitempty"`

	// providerNames is the comma-joined list of provider names, for the
	// PROVIDERS printer column: a JSONPath over providers[*].name renders
	// only its first match in kubectl's table view.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:MinLength=0
	ProviderNames string `json:"providerNames,omitempty"`

	// reason is the worst cloud-neutral reason across providers, empty when
	// healthy.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=0
	Reason string `json:"reason,omitempty"`

	// message explains the aggregate reason without changing its taxonomy.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:MinLength=0
	Message string `json:"message,omitempty"`

	// observedGeneration is the most recent CompositeCluster generation reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:resource:path=compositeclusters,shortName=cocl,scope=Namespaced,categories=cluster-api
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".metadata.labels['cluster\\.x-k8s\\.io/cluster-name']",description="Owning CAPI Cluster"
// +kubebuilder:printcolumn:name="Providers",type=string,JSONPath=".status.providerNames",description="Clouds this cluster spans"
// +kubebuilder:printcolumn:name="Provisioned",type=string,JSONPath=".status.initialization.provisioned",description="All children provisioned and endpoint fanned out"
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=".spec.controlPlaneEndpoint.host",description="Control plane endpoint"
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=".status.reason",description="Neutral failure reason"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// CompositeCluster is the Schema for the compositeclusters API.
//
// It implements the CAPI InfraCluster contract. Namespaced CompositeProviders
// select the native infrastructure and bootstrap children; this resource carries
// their shared endpoint and bootstrap configuration.
type CompositeCluster struct {
	metav1.TypeMeta `json:",inline"`
	// metadata identifies the resource and its ownership.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the desired infrastructure composition.
	// +required
	Spec CompositeClusterSpec `json:"spec,omitempty"`
	// status reports observed infrastructure state.
	// +optional
	Status CompositeClusterStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// CompositeClusterList contains a list of CompositeCluster.
type CompositeClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CompositeCluster `json:"items"`
}
