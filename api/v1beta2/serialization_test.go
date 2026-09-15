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
	"encoding/json"
	"reflect"
	"testing"
)

const serializationSpecJSON = `{"bootstrap":{"nested":{"remove":null,"array":[null,{"ratio":1.25},[]]}}}`

const serializationClusterJSON = `{"metadata":{"labels":{"test":"original"},"annotations":{"test":"original"},"finalizers":["test"]},
"spec":` + serializationSpecJSON + `,"status":{
"conditions":[{"type":"Ready","status":"False","lastTransitionTime":"2026-09-09T00:00:00Z","reason":"Pending","message":"pending"}],
"initialization":{"provisioned":false},"failureDomains":[{"name":"zone","controlPlane":false,"attributes":{"test":"original"}}],
"providers":[{"name":"test","provisioned":false,"outputs":{"test":"original"},
"childRef":{"apiVersion":"infra.example.test/v1beta1","kind":"TestCluster","name":"original"},
"bootstrapTemplateRef":{"apiVersion":"bootstrap.cluster.x-k8s.io/v1beta2","kind":"KubeadmConfigTemplate","name":"original"}}]}}`

func TestJSONSerialization(t *testing.T) {
	for _, tt := range []struct {
		name        string
		value       any
		input, want string
	}{
		{"initialization_absent", &InitializationStatus{}, `{}`, ``},
		{"initialization_false", &InitializationStatus{}, `{"provisioned":false}`, ``},
		{"provider_absent", &ProviderStatus{}, `{"name":"test"}`, ``},
		{"provider_false", &ProviderStatus{}, `{"name":"test","provisioned":false}`, ``},
		{"provider_node_pools", &ProviderStatus{}, `{"name":"test","nodePools":[{"apiVersion":"cluster.x-k8s.io/v1beta2","kind":"MachineDeployment","name":"workers"}]}`, ``},
		{"profile_reference", &ProviderSpec{}, `{"name":"regional","providerRef":{"name":"profile-a"}}`, ``},
		{"profile_empty_overrides", &ProviderSpec{}, `{"name":"regional","providerRef":{"name":"profile-a"},"infraSpec":{},"bootstrap":{}}`, ``},
		{"legacy_without_reference", &ProviderSpec{}, `{"name":"fake","region":"region-a","infraSpec":{}}`, ``},
		{"empty_cluster_spec", &CompositeClusterSpec{}, `{}`, ``},
		{"empty_cluster_template_resource", &CompositeClusterTemplateResource{}, `{"spec":{}}`, ``},
		{"empty_cluster_template", &CompositeClusterTemplate{}, `{}`, `{"metadata":{},"spec":{"template":{"spec":{}}}}`},
		{"credentials_reference", &ProviderCredentialsReference{}, `{"name":"regional-credentials","namePaths":["/credentials/fromSecret"]}`, ``},
		{"control_plane_absent", &FailureDomain{}, `{"name":"zone"}`, ``},
		{"control_plane_false", &FailureDomain{}, `{"name":"zone","controlPlane":false}`, ``},
		{"empty_endpoint", &CompositeClusterSpec{}, `{"controlPlaneEndpoint":{},` + serializationSpecJSON[1:], serializationSpecJSON},
		{"populated_endpoint", &CompositeClusterSpec{}, `{"controlPlaneEndpoint":{"host":"api.example.test","port":6443},` + serializationSpecJSON[1:], ``},
		{"empty_template_metadata", &CompositeClusterTemplateResource{}, `{"metadata":{},"spec":` + serializationSpecJSON + `}`, `{"spec":` + serializationSpecJSON + `}`},
		{"empty_template_maps", &CompositeClusterTemplateResource{}, `{"metadata":{"labels":{},"annotations":{}},"spec":` + serializationSpecJSON + `}`, `{"spec":` + serializationSpecJSON + `}`},
		{"populated_template_metadata", &CompositeClusterTemplateResource{}, `{"metadata":{"labels":{"test":"original"},"annotations":{"test":"original"}},"spec":` + serializationSpecJSON + `}`, ``},
		{"empty_initialization", &CompositeClusterStatus{}, `{"initialization":{}}`, `{}`},
		{"empty_status", &CompositeCluster{}, `{"spec":` + serializationSpecJSON + `,"status":{}}`, `{"metadata":{},"spec":` + serializationSpecJSON + `}`},
		{"zero_child_reference", &ChildReference{}, `{}`, ``},
		{"zero_cluster", &CompositeCluster{}, `{}`, `{"metadata":{},"spec":{}}`},
		{"opaque_cluster_round_trip", &CompositeCluster{}, serializationClusterJSON, ``},
	} {
		t.Run(tt.name, func(t *testing.T) {
			decodeSerializationJSON(t, tt.input, tt.value)
			if tt.want == "" {
				tt.want = tt.input
			}
			var got, want any
			decodeSerializationJSON(t, encodeSerializationJSON(t, tt.value), &got)
			decodeSerializationJSON(t, tt.want, &want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("JSON = %v; want %v", got, want)
			}
		})
	}
}

func TestClusterSpecHasNoProviderSelection(t *testing.T) {
	if _, exists := reflect.TypeFor[CompositeClusterSpec]().FieldByName("Providers"); exists {
		t.Fatal("provider selection must not be embedded in the public cluster spec")
	}
}

func TestRenderingProviderDeepCopy(t *testing.T) {
	var provider ProviderSpec
	decodeSerializationJSON(
		t,
		`{"name":"test","providerRef":{"name":"profile-a"},"region":"region","zones":["zone"],"endpointPaths":["spec.controlPlaneEndpoint"],"infraSpec":{"nested":{"remove":null,"array":[null,{"ratio":2.5},[]]}},"bootstrap":{"nested":{"remove":null,"array":[null,{"ratio":3.75},[]]}}}`,
		&provider,
	)
	before := encodeSerializationJSON(t, &provider)
	copied := provider.DeepCopy()
	copied.Name = "changed"
	copied.ProviderRef.Name = "changed"
	copied.InfraSpec.Raw[2] = 'X'
	copied.Bootstrap.Raw[2] = 'X'
	copied.Zones[0] = "changed"
	copied.EndpointPaths[0] = "spec.changed"
	if encodeSerializationJSON(t, &provider) != before {
		t.Fatal("mutating a rendering provider copy changed the original")
	}
}

func TestProfileStatusDeepCopy(t *testing.T) {
	var cluster CompositeCluster
	decodeSerializationJSON(
		t,
		`{"spec":{},"status":{"providers":[{"name":"regional","profile":{"name":"profile-a","provider":"fake","account":"account-a","region":"region-a"},"nodePools":[{"apiVersion":"cluster.x-k8s.io/v1beta2","kind":"MachineDeployment","name":"workers"}],"conditions":[{"type":"Ready","status":"True","reason":"Ready","observedGeneration":1,"lastTransitionTime":"2026-09-14T00:00:00Z"}]}]}}`,
		&cluster,
	)
	before := encodeSerializationJSON(t, &cluster)
	copied := cluster.DeepCopy()
	copied.Status.Providers[0].Profile.Name = "changed"
	copied.Status.Providers[0].Conditions[0].Reason = "Changed"
	copied.Status.Providers[0].NodePools[0].Name = "changed"
	if encodeSerializationJSON(t, &cluster) != before {
		t.Fatal("mutating a profile reference/status copy changed the original")
	}
}

func TestCredentialsReferenceDeepCopy(t *testing.T) {
	var original CompositeProviderTemplate
	decodeSerializationJSON(t, `{"spec":{"credentialsRef":{"name":"regional-credentials","namePaths":["/identityRef/name"]}}}`, &original)
	before := encodeSerializationJSON(t, &original)
	copied := original.DeepCopy()
	copied.Spec.CredentialsRef.NamePaths[0] = "/changed"
	if encodeSerializationJSON(t, &original) != before {
		t.Fatal("mutating credential paths in a deep copy changed the original")
	}
}

func TestDeepCopyIndependence(t *testing.T) {
	var cluster CompositeCluster
	decodeSerializationJSON(t, serializationClusterJSON, &cluster)
	clusters := CompositeClusterList{Items: []CompositeCluster{cluster}}
	template := CompositeClusterTemplate{ObjectMeta: cluster.ObjectMeta}
	template.Spec.Template = CompositeClusterTemplateResource{
		ObjectMeta: ObjectMeta{Labels: map[string]string{"test": "original"}, Annotations: map[string]string{"test": "original"}},
		Spec:       cluster.Spec,
	}
	templates := CompositeClusterTemplateList{Items: []CompositeClusterTemplate{template}}
	originals := []any{&cluster, &clusters, &template, &templates}
	before := encodeSerializationJSON(t, originals)
	mutateSpec := func(spec *CompositeClusterSpec) {
		spec.Bootstrap.Raw[2] = 'X'
	}
	for name, copied := range map[string]*CompositeCluster{
		"cluster": cluster.DeepCopy(), "cluster_list": &clusters.DeepCopy().Items[0],
	} {
		t.Run(name, func(t *testing.T) {
			if !reflect.DeepEqual(&cluster, copied) {
				t.Fatal("deep copy differs from input")
			}
			copied.Name = "changed"
			copied.Labels["test"], copied.Annotations["test"] = "changed", "changed"
			copied.Finalizers[0] = "changed"
			mutateSpec(&copied.Spec)
			copied.Status.Conditions[0].Reason = "Changed"
			*copied.Status.Initialization.Provisioned = true
			*copied.Status.FailureDomains[0].ControlPlane = true
			copied.Status.FailureDomains[0].Attributes["test"] = "changed"
			provider := &copied.Status.Providers[0]
			provider.Name, provider.ChildRef.Name, provider.BootstrapTemplateRef.Name = "changed", "changed", "changed"
			*provider.Provisioned = true
			provider.Outputs["test"] = "changed"
			if encodeSerializationJSON(t, originals) != before {
				t.Error("mutating deep copy changed input")
			}
		})
	}
	for name, copied := range map[string]*CompositeClusterTemplate{
		"template": template.DeepCopy(), "template_list": &templates.DeepCopy().Items[0],
	} {
		t.Run(name, func(t *testing.T) {
			if !reflect.DeepEqual(&template, copied) {
				t.Fatal("deep copy differs from input")
			}
			copied.Name = "changed"
			copied.Labels["test"], copied.Annotations["test"] = "changed", "changed"
			copied.Finalizers[0] = "changed"
			copied.Spec.Template.ObjectMeta.Labels["test"] = "changed"
			copied.Spec.Template.ObjectMeta.Annotations["test"] = "changed"
			mutateSpec(&copied.Spec.Template.Spec)
			if encodeSerializationJSON(t, originals) != before {
				t.Error("mutating deep copy changed input")
			}
		})
	}
}

func encodeSerializationJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func decodeSerializationJSON(t *testing.T, data string, value any) {
	t.Helper()
	if err := json.Unmarshal([]byte(data), value); err != nil {
		t.Fatal(err)
	}
}
