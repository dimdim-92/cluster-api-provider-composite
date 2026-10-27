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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestProviderAttachmentJSONAndDeepCopy(t *testing.T) {
	input := `{"metadata":{"name":"paris","namespace":"tenant"},"spec":{"clusterRef":{"name":"tenant-infra"},"templateRef":{"name":"paris-v1"},"credentialsRef":{"name":"native-credentials"},"infraSpec":{"big":9007199254740993},"bootstrap":{"preKubeadmCommands":["echo ready"]}},"status":{"initialization":{"provisioned":false}}}`
	var p CompositeProvider
	decodeSerializationJSON(t, input, &p)
	before := encodeSerializationJSON(t, &p)
	if !strings.Contains(before, "9007199254740993") || !strings.Contains(before, `"provisioned":false`) {
		t.Fatal("exact numbers and explicit optional false must survive serialization")
	}
	copied := p.DeepCopy()
	copied.Spec.InfraSpec.Raw[0] = '['
	copied.Spec.Bootstrap.Raw[0] = '['
	*copied.Status.Initialization.Provisioned = true
	copied.Spec.CredentialsRef.Name = "other"
	if encodeSerializationJSON(t, &p) != before {
		t.Fatal("a provider deepcopy mutated its original")
	}
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"CompositeProvider", "CompositeProviderList", "CompositeProviderTemplate", "CompositeProviderTemplateList"} {
		if _, err := scheme.New(GroupVersion.WithKind(kind)); err != nil {
			t.Fatal(err)
		}
	}
}
