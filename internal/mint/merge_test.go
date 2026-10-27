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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/numspot/cluster-api-provider-composite/internal/mint"
)

func json(raw string) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: []byte(raw)}
}

func TestMergePatch(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base  map[string]any
		patch *apiextensionsv1.JSON
		want  map[string]any
	}{
		"nil patch is a no-op": {
			base:  map[string]any{"vmType": "tinav7.c4r8p2"},
			patch: nil,
			want:  map[string]any{"vmType": "tinav7.c4r8p2"},
		},
		"scalar overrides the shared base": {
			base:  map[string]any{"vmType": "tinav7.c4r8p2", "keypairName": "ops"},
			patch: json(`{"vmType":"tinav7.c32r128p1"}`),
			want:  map[string]any{"vmType": "tinav7.c32r128p1", "keypairName": "ops"},
		},
		"nested objects merge recursively": {
			base: map[string]any{
				"node": map[string]any{
					"vm":    map[string]any{"vmType": "tinav7.c4r8p2", "subregionName": "eu-west-2a"},
					"image": map[string]any{"name": "ubuntu"},
				},
			},
			patch: json(`{"node":{"vm":{"keypairName":"ops"}}}`),
			want: map[string]any{
				"node": map[string]any{
					"vm": map[string]any{
						"vmType": "tinav7.c4r8p2", "subregionName": "eu-west-2a", "keypairName": "ops",
					},
					"image": map[string]any{"name": "ubuntu"},
				},
			},
		},
		"null deletes the key": {
			base:  map[string]any{"vmType": "tinav7.c4r8p2", "bastion": map[string]any{"enable": true}},
			patch: json(`{"bastion":null}`),
			want:  map[string]any{"vmType": "tinav7.c4r8p2"},
		},
		"nested null over an absent base recurses against an empty object": {
			base:  map[string]any{},
			patch: json(`{"node":{"deleted":null,"nested":{"deleted":null,"kept":"value"}}}`),
			want:  map[string]any{"node": map[string]any{"nested": map[string]any{"kept": "value"}}},
		},
		"nested null over a scalar base recurses against an empty object": {
			base:  map[string]any{"node": "old"},
			patch: json(`{"node":{"deleted":null,"nested":{"deleted":null}}}`),
			want:  map[string]any{"node": map[string]any{"nested": map[string]any{}}},
		},
		"nested null over a null base recurses against an empty object": {
			base:  map[string]any{"node": nil},
			patch: json(`{"node":{"nested":{"deleted":null}}}`),
			want:  map[string]any{"node": map[string]any{"nested": map[string]any{}}},
		},
		"nested null over an array base recurses against an empty object": {
			base:  map[string]any{"node": []any{"old"}},
			patch: json(`{"node":{"deleted":null}}`),
			want:  map[string]any{"node": map[string]any{}},
		},
		"arrays are replaced wholesale, never merged": {
			// The documented sharp edge: an overlay adding one rule silently
			// drops every rule the shared payload carries, including the
			// cross-cloud ones a spanning cluster needs.
			base:  map[string]any{"securityGroupRules": []any{"rule-a", "rule-b"}},
			patch: json(`{"securityGroupRules":["rule-c"]}`),
			want:  map[string]any{"securityGroupRules": []any{"rule-c"}},
		},
		"type change replaces": {
			base:  map[string]any{"net": map[string]any{"ipRange": "10.0.0.0/16"}},
			patch: json(`{"net":"existing-net-id"}`),
			want:  map[string]any{"net": "existing-net-id"},
		},
		"name-keyed lists merge by name — the v1beta2 list-map convention": {
			// kubeletExtraArgs became a list of {name, value} in v1beta2; the
			// per-provider overlay carries ONE entry and must not wipe the
			// shared ones. Base order kept, overlay wins per name, new names
			// appended.
			base: map[string]any{"kubeletExtraArgs": []any{
				map[string]any{"name": "cloud-provider", "value": "external"},
				map[string]any{"name": "node-labels", "value": "a=b"},
			}},
			patch: json(`{"kubeletExtraArgs":[{"name":"node-labels","value":"a=c"},{"name":"provider-id","value":"aws:///x"}]}`),
			want: map[string]any{"kubeletExtraArgs": []any{
				map[string]any{"name": "cloud-provider", "value": "external"},
				map[string]any{"name": "node-labels", "value": "a=c"},
				map[string]any{"name": "provider-id", "value": "aws:///x"},
			}},
		},
		"a list with an unnamed element still replaces wholesale": {
			base:  map[string]any{"files": []any{map[string]any{"name": "a"}, "plain"}},
			patch: json(`{"files":[{"name":"b"}]}`),
			want:  map[string]any{"files": []any{map[string]any{"name": "b"}}},
		},
		"named list entries replace whole objects and preserve literal nulls": {
			base: map[string]any{"args": []any{
				map[string]any{"name": "kept", "value": "base"},
				map[string]any{"name": "replaced", "value": "old", "extra": "remove"},
			}},
			patch: json(`{"args":[{"name":"replaced","value":null,"nested":{"literal":null}},{"name":"new","value":"added"}]}`),
			want: map[string]any{"args": []any{
				map[string]any{"name": "kept", "value": "base"},
				map[string]any{"name": "replaced", "value": nil, "nested": map[string]any{"literal": nil}},
				map[string]any{"name": "new", "value": "added"},
			}},
		},
		"patch may add new keys": {
			base:  map[string]any{"vmType": "tinav7.c4r8p2"},
			patch: json(`{"tags":{"team":"platform"}}`),
			want: map[string]any{
				"vmType": "tinav7.c4r8p2", "tags": map[string]any{"team": "platform"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			before := runtime.DeepCopyJSON(tc.base)
			got, err := mint.MergePatch(tc.base, tc.patch)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, before, tc.base, "merging must not mutate the input")
			again, err := mint.MergePatch(got, tc.patch)
			require.NoError(t, err)
			assert.Equal(t, got, again, "applying an overlay twice must be idempotent")
		})
	}
}

func TestMergeObjects_OverlayIndependence(t *testing.T) {
	t.Parallel()
	for name, merge := range map[string]func(map[string]any, map[string]any) map[string]any{
		"bootstrap":      mint.MergeBootstrapObjects,
		"infrastructure": mint.MergeInfrastructureObjects,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := map[string]any{"args": []any{map[string]any{"name": "base", "value": "kept"}}}
			overlay := map[string]any{"args": []any{map[string]any{"name": "added", "value": "overlay"}}}
			baseBefore, overlayBefore := runtime.DeepCopyJSON(base), runtime.DeepCopyJSON(overlay)
			got := merge(base, overlay)
			args, ok := got["args"].([]any)
			require.True(t, ok)
			require.NotEmpty(t, args)
			added, ok := args[len(args)-1].(map[string]any)
			require.True(t, ok)
			added["value"] = "changed"
			assert.Equal(t, baseBefore, base)
			assert.Equal(t, overlayBefore, overlay)
		})
	}
}

func TestMergePatch_InputIndependence(t *testing.T) {
	t.Parallel()

	for name, patch := range map[string]*apiextensionsv1.JSON{
		"nil": nil, "empty": {}, "empty object": json(`{}`),
		"named list": json(`{"args":[{"name":"added","value":"overlay"}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := map[string]any{
				"object": map[string]any{"kept": "base"},
				"array":  []any{map[string]any{"kept": "base"}},
				"args":   []any{map[string]any{"name": "kept", "value": "base"}},
			}
			before := runtime.DeepCopyJSON(base)
			got, err := mint.MergePatch(base, patch)
			require.NoError(t, err)
			other, err := mint.MergePatch(base, patch)
			require.NoError(t, err)
			otherBefore := runtime.DeepCopyJSON(other)
			object, ok := got["object"].(map[string]any)
			require.True(t, ok)
			object["kept"] = "changed"
			array, ok := got["array"].([]any)
			require.True(t, ok)
			require.NotEmpty(t, array)
			arrayEntry, ok := array[0].(map[string]any)
			require.True(t, ok)
			arrayEntry["kept"] = "changed"
			args, ok := got["args"].([]any)
			require.True(t, ok)
			require.NotEmpty(t, args)
			arg, ok := args[0].(map[string]any)
			require.True(t, ok)
			arg["value"] = "changed"
			got["new"] = "changed"
			assert.Equal(t, before, base, "mutating a result must not alter the shared base")
			assert.Equal(t, otherBefore, other, "one provider's result must not alter another's")
		})
	}
}

func TestMergePatch_DoesNotMutateBase(t *testing.T) {
	t.Parallel()

	// The shared base payload is reused across every provider in a reconcile.
	// An in-place merge would let one provider's overlay leak into another's
	// cascade.
	base := map[string]any{
		"node": map[string]any{"vm": map[string]any{"vmType": "tinav7.c4r8p2"}},
	}

	_, err := mint.MergePatch(base, json(`{"node":{"vm":{"vmType":"changed"}}}`))
	require.NoError(t, err)

	node, ok := base["node"].(map[string]any)
	require.True(t, ok)

	vm, ok := node["vm"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "tinav7.c4r8p2", vm["vmType"], "base must be untouched")
}

func TestMergePatch_RejectsMalformedPatch(t *testing.T) {
	t.Parallel()

	_, err := mint.MergePatch(map[string]any{}, json(`{"unbalanced":`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing merge patch")
}
