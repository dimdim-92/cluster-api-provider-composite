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

package mint_test

import (
	stdjson "encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/mint"
)

func TestInlineRendering_PreservesExactNumbers(t *testing.T) {
	t.Parallel()
	for _, number := range []string{
		"9007199254740992", "9007199254740993", "-9007199254740993",
		"18446744073709551615", "0.12345678901234567890123456789", "1e1000000",
	} {
		for _, source := range []string{"infrastructure", "shared-bootstrap", "bootstrap-override"} {
			t.Run(source+"/"+number, func(t *testing.T) {
				t.Parallel()
				raw := fmt.Sprintf(`{"exact":%s,"values":[%s]}`, number, number)
				cc := &infrav1.CompositeCluster{}
				p := &infrav1.ProviderSpec{Name: "fake"}
				var obj *unstructured.Unstructured
				var err error
				path := []string{"spec"}
				switch source {
				case "infrastructure":
					p.InfraSpec = *json(raw)
					obj, err = mint.RenderInfraCluster(cc, p, schema.GroupVersionKind{Group: "infra.test", Version: "v1", Kind: "FakeCluster"})
				case "shared-bootstrap":
					cc.Spec.Bootstrap = json(raw)
					obj, err = mint.RenderClusterKCT(cc, p)
					path = []string{"spec", "template", "spec"}
				case "bootstrap-override":
					p.Bootstrap = json(raw)
					obj, err = mint.RenderClusterKCT(cc, p)
					path = []string{"spec", "template", "spec"}
				}
				require.NoError(t, err)
				payload, found, err := unstructured.NestedMap(obj.Object, path...)
				require.NoError(t, err)
				require.True(t, found)
				encoded, err := stdjson.Marshal(payload)
				require.NoError(t, err)
				assert.Equal(t, raw, string(encoded), "compare exact JSON text; JSONEq would itself round large numbers")
			})
		}
	}
}

func TestInlineRendering_RejectsTrailingJSON(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{} {}`, `{} null`, `{} trailing`, `{}]`} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			p := &infrav1.ProviderSpec{Name: "fake", InfraSpec: *json(raw)}
			_, err := mint.RenderInfraCluster(&infrav1.CompositeCluster{}, p, schema.GroupVersionKind{})
			require.Error(t, err)
			_, err = mint.MergePatch(map[string]any{}, json(raw))
			require.Error(t, err)
		})
	}
}
