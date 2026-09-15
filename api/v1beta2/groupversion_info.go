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

// Package v1beta2 contains the API schema for the capi-composite infrastructure
// provider.
//
// The group is infrastructure.cluster.x-k8s.io. CompositeCluster and its template
// advertise the CAPI infrastructure contract. CompositeProvider is a namespaced
// attachment and CompositeProviderTemplate is shared configuration. Neither
// advertises the InfraCluster contract.
// See docs/design.md for the resource and ownership model.
//
// This package uses API machinery and the required opaque JSON type. It is a separate Go
// module so downstream consumers can import the types without pulling in
// controller-runtime or Cluster API. Contract structs that would otherwise come
// from sigs.k8s.io/cluster-api are redeclared here with byte-identical JSON —
// see APIEndpoint and FailureDomain in common_types.go.
//
// +kubebuilder:object:generate=true
// +groupName=infrastructure.cluster.x-k8s.io
package v1beta2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta2"}

	// SchemeBuilder collects the functions that add these types to a Scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// Resource takes an unqualified resource and returns a Group-qualified GroupResource.
func Resource(resource string) schema.GroupResource {
	return GroupVersion.WithResource(resource).GroupResource()
}

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&CompositeCluster{}, &CompositeClusterList{},
		&CompositeClusterTemplate{}, &CompositeClusterTemplateList{},
		&CompositeProvider{}, &CompositeProviderList{},
		&CompositeProviderTemplate{}, &CompositeProviderTemplateList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)

	return nil
}
