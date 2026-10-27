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

package envtest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

// The CEL rules on these types are real validation logic, enforced by the API
// server rather than by a webhook we have to run. Testing them here means they
// are checked against the CRDs `make gen` actually emits, not against the
// markers we think we wrote.

func rawJSON(s string) apiextensionsv1.JSON {
	return apiextensionsv1.JSON{Raw: []byte(s)}
}

func TestCompositeCluster_EmptySpecAndNoLegacyProviderSchema(t *testing.T) {
	for _, kind := range []string{"CompositeCluster", "CompositeClusterTemplate"} {
		t.Run(kind, func(t *testing.T) {
			plural := "compositeclusters"
			if kind == "CompositeClusterTemplate" {
				plural = "compositeclustertemplates"
			}
			crd := &apiextensionsv1.CustomResourceDefinition{}
			require.NoError(t, k8s.Get(t.Context(), client.ObjectKey{Name: plural + "." + infrav1.GroupVersion.Group}, crd))
			for _, version := range crd.Spec.Versions {
				spec := version.Schema.OpenAPIV3Schema.Properties["spec"]
				if kind == "CompositeClusterTemplate" {
					spec = spec.Properties["template"].Properties["spec"]
				}
				assert.NotContains(t, spec.Properties, "providers")
				assert.NotContains(t, spec.Required, "providers")
			}
			obj := &unstructured.Unstructured{}
			obj.SetAPIVersion(infrav1.GroupVersion.String())
			obj.SetKind(kind)
			obj.SetName("empty")
			obj.SetNamespace(newNamespace(t))
			if kind == "CompositeClusterTemplate" {
				obj.Object["spec"] = map[string]any{"template": map[string]any{"spec": map[string]any{}}}
			} else {
				obj.Object["spec"] = map[string]any{}
			}
			require.NoError(t, k8s.Create(t.Context(), obj))
		})
	}
}

// sanitise turns a test name into a DNS-1123 object name.
func sanitise(s string) string {
	out := make([]rune, 0, len(s))

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
		default:
			out = append(out, '-')
		}
	}

	return string(out)
}
