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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CompositeProviderReference names an immutable CompositeProviderTemplate in the cluster-wide catalog.
type CompositeProviderReference struct {
	// name is the profile resource name; no namespace is required.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name,omitempty"`
}

// ProviderProfileStatus reflects configuration identity from a durable attachment
// pin. It contains no credential values or opaque payload snapshots.
type ProviderProfileStatus struct {
	// name is the referenced immutable profile revision.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`
	// provider is the logical native provider recorded by the profile.
	// +required
	Provider ProviderName `json:"provider,omitempty"`
	// account is the profile's administrative account identity.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Account string `json:"account,omitempty"`
	// region is the profile's native region identity.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Region string `json:"region,omitempty"`
}

// NativeInfraClusterKind identifies an external infrastructure API, independently
// of its served version. It cannot reference a Composite configuration/resource kind.
// +kubebuilder:validation:XValidation:rule="self.apiGroup != 'infrastructure.cluster.x-k8s.io' || !(self.kind in ['CompositeCluster', 'CompositeClusterTemplate', 'CompositeProvider', 'CompositeProviderTemplate'])",message="native target cannot be a Composite resource"
type NativeInfraClusterKind struct {
	// apiGroup is the native resource's API group.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	APIGroup string `json:"apiGroup,omitempty"`
	// kind is the native InfraCluster resource kind, not a template or profile.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[A-Za-z][A-Za-z0-9]*$`
	Kind string `json:"kind,omitempty"`
}

// ProfileJSONPointer identifies an object property or subtree in native defaults.
// It uses JSON Pointer escaping (~0 and ~1), not wildcard or list-selector syntax.
// An empty/root pointer and empty path segments are not supported.
// +kubebuilder:validation:MinLength=2
// +kubebuilder:validation:MaxLength=512
// +kubebuilder:validation:Pattern=`^(/([^~/*]|~[01])+)+$`
type ProfileJSONPointer string

// ProviderTemplateReference selects an existing namespaced template revision.
// Only spec.template.spec is consumed; source ownership and metadata are not copied.
type ProviderTemplateReference struct {
	// apiVersion is the explicitly served group/version of the source template.
	// +required
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=317
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?/[a-z][a-z0-9]*$`
	APIVersion string `json:"apiVersion,omitempty"`
	// kind is the source template kind, such as OscClusterTemplate.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[A-Za-z][A-Za-z0-9]*$`
	Kind string `json:"kind,omitempty"`
	// namespace contains the shared source template.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace,omitempty"`
	// name identifies an immutable, administratively managed template revision.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name,omitempty"`
}

// ProviderCredentialsReference selects a native provider Secret by name without
// reading its contents. The native template must use namespace-local credentials.
type ProviderCredentialsReference struct {
	// name identifies the Secret in the consuming CompositeCluster namespace.
	// Secret contents remain native-provider configuration and may rotate in place.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name,omitempty"`
	// namePaths are JSON pointers relative to the native InfraCluster spec where
	// the Secret name is written, for example /credentials/fromSecret or /identityRef/name.
	// They address object properties, never array elements, and cannot overlap
	// each other or endpoint targets. Consumer overrides cannot change these fields.
	// +required
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	NamePaths []ProfileJSONPointer `json:"namePaths,omitempty"`
}

// CompositeProviderOverridePolicy declares mutable and protected configuration
// subtrees. An omitted or empty policy permits no consumer overrides. Effective
// override evaluation belongs to reference reconciliation, not this schema alone.
// +kubebuilder:validation:MinProperties=0
type CompositeProviderOverridePolicy struct {
	// infraSpecPaths are infrastructure subtrees that consumers may override.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=32
	InfraSpecPaths []ProfileJSONPointer `json:"infraSpecPaths,omitempty"`
	// bootstrapPaths are bootstrap subtrees that consumers may override.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=32
	BootstrapPaths []ProfileJSONPointer `json:"bootstrapPaths,omitempty"`
	// protectedInfraSpecPaths retain native identity/settings even under an allowed ancestor.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=32
	ProtectedInfraSpecPaths []ProfileJSONPointer `json:"protectedInfraSpecPaths,omitempty"`
	// protectedBootstrapPaths retain profile-owned bootstrap settings.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=32
	ProtectedBootstrapPaths []ProfileJSONPointer `json:"protectedBootstrapPaths,omitempty"`
}

// CompositeProviderTemplateSpec defines one immutable provider/account/region revision.
// Source template references and override policy are immutable together with identity.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="CompositeProviderTemplate spec is immutable; create a new named revision"
// +kubebuilder:validation:XValidation:rule="self.infrastructureTemplateRef.kind == self.infraClusterKind.kind + 'Template' && self.infrastructureTemplateRef.apiVersion.startsWith(self.infraClusterKind.apiGroup + '/')",message="infrastructure template must match the native InfraCluster kind and group"
// +kubebuilder:validation:XValidation:rule="self.bootstrapTemplateRef.kind == 'KubeadmConfigTemplate' && self.bootstrapTemplateRef.apiVersion == 'bootstrap.cluster.x-k8s.io/v1beta2'",message="bootstrap template must be a v1beta2 KubeadmConfigTemplate"
// +kubebuilder:validation:XValidation:rule="!has(self.credentialsNamePaths) || size(self.credentialsNamePaths) == 0 || !has(self.credentialsRef)",message="use attachment credential paths or fixed template credentials, not both"
type CompositeProviderTemplateSpec struct {
	// provider is the logical native provider identifier.
	// +required
	Provider ProviderName `json:"provider,omitempty"`
	// account is a non-secret administrative identity, not a verified cloud identity.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Account string `json:"account,omitempty"`
	// region identifies this profile's region; the native template must select that region.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Region string `json:"region,omitempty"`
	// credentialsRef optionally binds a namespace-local native Secret name to
	// declared fields in the rendered infrastructure spec. It is immutable with
	// this profile; source templates and Secret contents are never modified.
	// Omission preserves credential references from the native source template.
	// +optional
	CredentialsRef ProviderCredentialsReference `json:"credentialsRef,omitempty,omitzero"`
	// credentialsNamePaths declares protected Secret-name injection paths for
	// namespaced provider attachments. The attachment supplies the Secret name.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=8
	CredentialsNamePaths []ProfileJSONPointer `json:"credentialsNamePaths,omitempty"`
	// infraClusterKind identifies the native infrastructure API to resolve.
	// +required
	InfraClusterKind NativeInfraClusterKind `json:"infraClusterKind,omitempty,omitzero"`
	// zones are native regional zone names, with no global renaming or deduplication.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=256
	Zones []string `json:"zones,omitempty"`
	// infrastructureTemplateRef selects the native cluster template containing infrastructure defaults.
	// +required
	InfrastructureTemplateRef ProviderTemplateReference `json:"infrastructureTemplateRef,omitempty,omitzero"`
	// bootstrapTemplateRef selects a v1beta2 KubeadmConfigTemplate containing bootstrap defaults.
	// +required
	BootstrapTemplateRef ProviderTemplateReference `json:"bootstrapTemplateRef,omitempty,omitzero"`
	// endpointPaths selects controller-owned endpoint targets; empty uses spec.controlPlaneEndpoint.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=512
	// +kubebuilder:validation:items:Pattern=`^spec\.[a-zA-Z0-9_]+(\.[a-zA-Z0-9_]+)*$`
	EndpointPaths []string `json:"endpointPaths,omitempty"`
	// overridePolicy declares permitted and protected native subtrees.
	// +optional
	OverridePolicy *CompositeProviderOverridePolicy `json:"overridePolicy,omitempty,omitzero"`
}

// CompositeProviderTemplateStatus reports catalog observations, never workload provisioning.
// +kubebuilder:validation:MinProperties=1
type CompositeProviderTemplateStatus struct {
	// conditions describe configuration and API availability observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// observedGeneration is the most recent profile generation observed by a controller.
	// +optional
	// +kubebuilder:validation:Minimum=1
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:resource:path=compositeprovidertemplates,shortName=cpt,scope=Cluster,categories=cluster-api
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=".spec.provider"
// +kubebuilder:printcolumn:name="Account",type=string,JSONPath=".spec.account"
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=".spec.region"

// CompositeProviderTemplate is a cluster-wide immutable regional configuration profile.
// It is not a CAPI InfraCluster and does not own its consumers or their resources.
type CompositeProviderTemplate struct {
	metav1.TypeMeta `json:",inline"`
	// metadata identifies the immutable named revision.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// spec holds immutable provider defaults and override policy.
	// +required
	Spec CompositeProviderTemplateSpec `json:"spec,omitempty,omitzero"`
	// status holds observations about this catalog entry.
	// +optional
	Status CompositeProviderTemplateStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// CompositeProviderTemplateList contains CompositeProviderTemplate resources.
type CompositeProviderTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CompositeProviderTemplate `json:"items"`
}
