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

// CompositeClusterReference selects the infrastructure contract in this namespace.
type CompositeClusterReference struct {
	// name identifies the CompositeCluster, not the owning CAPI Cluster.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name,omitempty"`
}

// ProviderSecretReference selects namespace-local native credentials without reading them.
type ProviderSecretReference struct {
	// name identifies a Secret in the CompositeProvider namespace.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name,omitempty"`
}

// CompositeProviderSpec declares one template-backed provider attachment, independent of worker sizing.
// +kubebuilder:validation:XValidation:rule="self.clusterRef == oldSelf.clusterRef",message="clusterRef is immutable"
// +kubebuilder:validation:XValidation:rule="self.templateRef == oldSelf.templateRef",message="templateRef is immutable"
// +kubebuilder:validation:XValidation:rule="has(self.credentialsRef) == has(oldSelf.credentialsRef) && (!has(self.credentialsRef) || self.credentialsRef == oldSelf.credentialsRef)",message="credentialsRef is immutable"
// +kubebuilder:validation:XValidation:rule="has(self.infraClusterName) == has(oldSelf.infraClusterName) && (!has(self.infraClusterName) || self.infraClusterName == oldSelf.infraClusterName)",message="infraClusterName is immutable"
type CompositeProviderSpec struct {
	// clusterRef selects the CompositeCluster in this provider's namespace.
	// +required
	ClusterRef CompositeClusterReference `json:"clusterRef,omitempty,omitzero"`
	// templateRef selects an immutable cluster-wide CompositeProviderTemplate revision.
	// +required
	TemplateRef CompositeProviderReference `json:"templateRef,omitempty,omitzero"`
	// credentialsRef supplies the native Secret name. Injection paths belong to the template.
	// +optional
	CredentialsRef ProviderSecretReference `json:"credentialsRef,omitempty,omitzero"`
	// infraClusterName selects an explicit regional native name; omission uses clusterRef.name.
	// Every native GroupKind must retain a canonical child named after the CompositeCluster.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	InfraClusterName string `json:"infraClusterName,omitempty"`
	// infraSpec overrides only the native template subtrees allowed by the provider template.
	// +optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	InfraSpec apiextensionsv1.JSON `json:"infraSpec,omitempty,omitzero"`
	// bootstrap overrides only allowed bootstrap subtrees; workers remain native CAPI resources.
	// +optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Bootstrap *apiextensionsv1.JSON `json:"bootstrap,omitempty"`
}

// CompositeProviderStatus observes the native infrastructure and bootstrap, never worker availability.
// +kubebuilder:validation:MinProperties=1
type CompositeProviderStatus struct {
	// conditions describe current attachment health at the observed generation.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// initialization latches the first successful infrastructure and endpoint observation.
	// +optional
	Initialization InitializationStatus `json:"initialization,omitempty,omitzero"`
	// infrastructureRef identifies the native InfraCluster created for this attachment.
	// +optional
	InfrastructureRef ChildReference `json:"infrastructureRef,omitempty,omitzero"`
	// bootstrapTemplateRef is the generated template consumed by any number of native worker pools.
	// +optional
	BootstrapTemplateRef ChildReference `json:"bootstrapTemplateRef,omitempty,omitzero"`
	// profile reports the immutable template's administrative identity, not credential contents.
	// +optional
	Profile ProviderProfileStatus `json:"profile,omitempty,omitzero"`
	// observedGeneration is the most recent attachment generation observed by the controller.
	// +optional
	// +kubebuilder:validation:Minimum=1
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:resource:path=compositeproviders,shortName=cprov,scope=Namespaced,categories=cluster-api
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63",message="provider attachment names must be at most 63 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="provider attachment names must be DNS labels"
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=".spec.templateRef.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"

// CompositeProvider binds an MSC supplemental addon to provider-specific infrastructure and bootstrap.
// The CompositeCluster remains the controller owner of generated children; MSC and CAPI own workers.
type CompositeProvider struct {
	metav1.TypeMeta `json:",inline"`
	// metadata identifies the namespaced provider attachment.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// spec selects the cluster, immutable configuration and allowed overrides.
	// +required
	Spec CompositeProviderSpec `json:"spec,omitempty,omitzero"`
	// status reports current infrastructure/bootstrap observations.
	// +optional
	Status CompositeProviderStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// CompositeProviderList contains namespaced provider attachments.
type CompositeProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CompositeProvider `json:"items"`
}
