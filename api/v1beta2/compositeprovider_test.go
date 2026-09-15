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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

const profileJSON = `{"apiVersion":"infrastructure.cluster.x-k8s.io/v1beta2","kind":"CompositeProviderTemplate",
"metadata":{"name":"example-account-region-r1","labels":{"example":"original"}},
"spec":{"provider":"example","account":"account","region":"region",
"infraClusterKind":{"apiGroup":"infrastructure.example.test","kind":"ExampleCluster"},
"zones":["zone-a"],"endpointPaths":["spec.controlPlaneEndpoint"],
"infrastructureTemplateRef":{"apiVersion":"infrastructure.example.test/v1beta1","kind":"ExampleClusterTemplate","namespace":"catalog","name":"infrastructure-r1"},
"bootstrapTemplateRef":{"apiVersion":"bootstrap.cluster.x-k8s.io/v1beta2","kind":"KubeadmConfigTemplate","namespace":"catalog","name":"bootstrap-r1"},
"overridePolicy":{"infraSpecPaths":["/network"],"bootstrapPaths":["/files"],
"protectedInfraSpecPaths":["/identityRef"],"protectedBootstrapPaths":["/identity"]}},
"status":{"observedGeneration":1,"conditions":[{"type":"Available","status":"False",
"lastTransitionTime":"2026-09-14T00:00:00Z","reason":"Pending","message":"pending"}]}}`

func TestCompositeProviderJSONRoundTrip(t *testing.T) {
	var profile CompositeProviderTemplate
	decodeSerializationJSON(t, profileJSON, &profile)
	encoded := encodeSerializationJSON(t, &profile)
	decodeNumbers := func(input string) any {
		t.Helper()
		decoder := json.NewDecoder(strings.NewReader(input))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !reflect.DeepEqual(decodeNumbers(profileJSON), decodeNumbers(encoded)) {
		t.Fatal("profile JSON changed, including template references and override policy")
	}
	for _, object := range []any{&ProviderTemplateReference{}, &CompositeProviderOverridePolicy{}, &CompositeProviderTemplateStatus{}} {
		if got := encodeSerializationJSON(t, object); got != "{}" {
			t.Errorf("empty optional object = %s", got)
		}
	}
	empty := &CompositeProviderTemplate{}
	if got := encodeSerializationJSON(t, empty); got != `{"metadata":{}}` {
		t.Errorf("zero profile = %s", got)
	}
}

func TestCompositeProviderTemplateReferencesRoundTrip(t *testing.T) {
	input := `{"metadata":{"name":"regional-r1"},"spec":{"provider":"example","account":"account-a","region":"region-a","infraClusterKind":{"apiGroup":"infrastructure.example.test","kind":"ExampleCluster"},"infrastructureTemplateRef":{"apiVersion":"infrastructure.example.test/v1beta1","kind":"ExampleClusterTemplate","namespace":"catalog","name":"infrastructure-r1"},"bootstrapTemplateRef":{"apiVersion":"bootstrap.cluster.x-k8s.io/v1beta2","kind":"KubeadmConfigTemplate","namespace":"catalog","name":"bootstrap-r1"}}}`
	var profile CompositeProviderTemplate
	decodeSerializationJSON(t, input, &profile)
	var want, got map[string]any
	decodeSerializationJSON(t, input, &want)
	decodeSerializationJSON(t, encodeSerializationJSON(t, &profile), &got)
	if !reflect.DeepEqual(want, got) {
		t.Fatal("template references must survive an API round trip without embedded payloads")
	}
}

func TestCompositeProviderDeepCopyAndScheme(t *testing.T) {
	var profile CompositeProviderTemplate
	decodeSerializationJSON(t, profileJSON, &profile)
	before := encodeSerializationJSON(t, &profile)
	list := CompositeProviderTemplateList{Items: []CompositeProviderTemplate{profile}}
	for name, copied := range map[string]*CompositeProviderTemplate{
		"profile": profile.DeepCopy(), "list": &list.DeepCopy().Items[0],
	} {
		t.Run(name, func(t *testing.T) {
			if !reflect.DeepEqual(&profile, copied) {
				t.Fatal("copy differs before mutation")
			}
			copied.Labels["example"] = "changed"
			copied.Spec.Zones[0] = "changed"
			copied.Spec.EndpointPaths[0] = "spec.changed"
			copied.Spec.InfrastructureTemplateRef.Name = "another-template"
			copied.Spec.BootstrapTemplateRef.Namespace = "another-namespace"
			copied.Spec.OverridePolicy.InfraSpecPaths[0] = "/changed"
			copied.Spec.OverridePolicy.BootstrapPaths[0] = "/changed"
			copied.Spec.OverridePolicy.ProtectedInfraSpecPaths[0] = "/changed"
			copied.Spec.OverridePolicy.ProtectedBootstrapPaths[0] = "/changed"
			copied.Status.Conditions[0].Message = "changed"
			if before != encodeSerializationJSON(t, &profile) {
				t.Fatal("copy mutation changed source profile")
			}
		})
	}
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"CompositeProviderTemplate", "CompositeProviderTemplateList"} {
		object, err := scheme.New(GroupVersion.WithKind(kind))
		if err != nil {
			t.Fatal(err)
		}
		if object.DeepCopyObject() == nil {
			t.Fatalf("%s has no runtime deepcopy", kind)
		}
	}
}
