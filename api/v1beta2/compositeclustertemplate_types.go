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

// ObjectMeta is the subset of metav1.ObjectMeta that CAPI propagates from a
// template to the object it stamps out. Redeclared rather than imported from
// Cluster API to keep this module free of that dependency; the JSON shape
// matches clusterv1.ObjectMeta.
// +kubebuilder:validation:MinProperties=1
type ObjectMeta struct {
	// labels are propagated to generated CompositeClusters.
	// +optional
	// +kubebuilder:validation:MinProperties=0
	Labels map[string]string `json:"labels,omitempty"`
	// annotations are propagated to generated CompositeClusters.
	// +optional
	// +kubebuilder:validation:MinProperties=0
	Annotations map[string]string `json:"annotations,omitempty"`
}

// IsZero reports whether there is any metadata to propagate.
func (m ObjectMeta) IsZero() bool { return len(m.Labels) == 0 && len(m.Annotations) == 0 }

// CompositeClusterTemplateResource is the templated CompositeCluster.
type CompositeClusterTemplateResource struct {
	// metadata contains the labels and annotations applied to the generated resource.
	// +optional
	ObjectMeta ObjectMeta `json:"metadata,omitempty,omitzero"`
	// spec is the desired infrastructure composition.
	// +required
	Spec CompositeClusterSpec `json:"spec,omitempty"`
}

// CompositeClusterTemplateSpec defines the desired state of a
// CompositeClusterTemplate.
type CompositeClusterTemplateSpec struct {
	// template is immutable, matching the webhook-enforced behaviour of CAPO and
	// CAPOSC templates. Expressed as a CEL rule so no webhook is needed for it.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="CompositeClusterTemplate spec.template is immutable"
	Template CompositeClusterTemplateResource `json:"template,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=compositeclustertemplates,shortName=cct,scope=Namespaced,categories=cluster-api
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// CompositeClusterTemplate is the Schema for the compositeclustertemplates API.
//
// It exists so a ClusterClass can reference us as its infrastructure template.
// Adopting capi-composite in the existing Numspot ClusterClass is a one-line swap
// of OscClusterTemplate for CompositeClusterTemplate.
type CompositeClusterTemplate struct {
	metav1.TypeMeta `json:",inline"`
	// metadata identifies the template.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the immutable infrastructure template.
	// +required
	Spec CompositeClusterTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// CompositeClusterTemplateList contains a list of CompositeClusterTemplate.
type CompositeClusterTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CompositeClusterTemplate `json:"items"`
}
