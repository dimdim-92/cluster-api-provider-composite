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

const (
	// ClusterFinalizer is set on a CompositeCluster so provider children are torn
	// down in order rather than by unordered ownerRef garbage collection.
	// See docs/design.md for ownership and ordered deletion.
	ClusterFinalizer = "compositecluster.infrastructure.cluster.x-k8s.io"

	// ProviderInUseFinalizer retains a template while provider attachments reference it.
	ProviderInUseFinalizer = "compositeprovider.infrastructure.cluster.x-k8s.io/in-use"

	// ProviderNameLabel records the attachment key a generated child belongs to.
	ProviderNameLabel = "compositecluster.infrastructure.cluster.x-k8s.io/provider"

	// ProviderAttachmentsAnnotation stores namespaced provider identities and source pins.
	ProviderAttachmentsAnnotation = "compositecluster.infrastructure.cluster.x-k8s.io/provider-attachments"
	// ProviderBindingAnnotation pins an attachment to its infrastructure contract and sources.
	ProviderBindingAnnotation = "compositeprovider.infrastructure.cluster.x-k8s.io/binding"
	// ProviderAttachmentFinalizer retains infrastructure and bootstrap until workers are drained.
	ProviderAttachmentFinalizer = "compositeprovider.infrastructure.cluster.x-k8s.io/infrastructure"
)

// ProviderName is a DNS-label identifier used for native providers and attachment keys.
//
// This is deliberately an OPEN string and not a CRD enum. A closed enum makes
// adding a provider an API change requiring CRD regeneration and redeployment
// — adding a provider should not require changing these shared types.
// Validity reduces to "are the provider's child CRDs installed on this management
// cluster", surfaced as Ready=False/ProviderNotInstalled when they are not.
//
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=63
// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
type ProviderName string

// APIEndpoint is the control plane endpoint, part of the CAPI InfraCluster
// contract at spec.controlPlaneEndpoint.
//
// Redeclared here rather than imported from sigs.k8s.io/cluster-api so this
// module stays importable without Cluster API's dependency tree. CAPI reads the
// field through unstructured paths, so only the JSON shape matters and this is
// byte-identical to clusterv1.APIEndpoint.
// +kubebuilder:validation:MinProperties=1
type APIEndpoint struct {
	// host is the hostname on which the API server is serving.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Host string `json:"host,omitempty"`

	// port is the port on which the API server is serving.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
}

// IsValid reports whether both host and port are set.
func (e APIEndpoint) IsValid() bool { return e.Host != "" && e.Port > 0 && e.Port <= 65535 }

// IsZero reports whether neither host nor port is set.
func (e APIEndpoint) IsZero() bool { return e.Host == "" && e.Port == 0 }

// FailureDomain is a CAPI contract type published at status.failureDomains.
// Byte-identical to clusterv1.FailureDomain; see APIEndpoint for why it is
// redeclared.
//
// A spanning cluster publishes the UNION across its clouds, which is how a
// single contract field advertises availability zones from two providers.
type FailureDomain struct {
	// name is the provider's own name for the failure domain, e.g. "eu-west-2a".
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name,omitempty"`

	// controlPlane determines if this failure domain is suitable for control
	// plane machines. Always false here: hosted control planes do not use the
	// workload infrastructure's failure domains.
	// +optional
	ControlPlane *bool `json:"controlPlane,omitempty"`

	// attributes records provider, region and zone, plus account and attachment
	// for profile-backed entries. These observations do not alter native worker lookup.
	// +optional
	// +kubebuilder:validation:MinProperties=0
	Attributes map[string]string `json:"attributes,omitempty"`
}

// ChildReference is a concrete, versioned reference to an observed native
// object, including generated children and externally managed node pools.
//
// Unlike CAPI's ContractVersionedObjectReference this keeps the apiVersion: we
// resolve the child's contract version ourselves from its CRD labels and
// recording the resolved version makes the object self-describing when
// debugging. See docs/design.md for contract resolution.
type ChildReference struct {
	// apiVersion is the observed served group/version, not durable resource identity.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=317
	APIVersion string `json:"apiVersion,omitempty"`
	// kind is the referenced resource kind.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Kind string `json:"kind,omitempty"`
	// name is the referenced object's name in the CompositeCluster namespace.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`
}
