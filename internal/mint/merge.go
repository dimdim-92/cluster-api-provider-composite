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

// Package mint renders provider-native children from opaque payloads
// without translating provider fields. Rendering is pure: the caller owns metadata, ownership,
// validation and the apply.
package mint

import (
	"fmt"
	"maps"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// MergePatch applies an RFC 7386 JSON Merge Patch to a payload map. It is the
// bootstrap cascade's engine: the shared base payload merged with a
// per-provider overlay, the overlay always winning.
// The result is independent of the input, including when the patch is empty.
//
// SEMANTICS, which are worth stating plainly because one of them surprises
// people:
//
//   - objects merge recursively;
//   - a null value DELETES the key;
//   - arrays are REPLACED WHOLESALE, never merged element-wise — with ONE
//     extension: a list whose every element is an object carrying a "name"
//     key (the v1beta2 list-map convention: kubeletExtraArgs, extraArgs,
//     env, ...) merges by name, the overlay winning per name and new names
//     appended in overlay order.
//
// The extension exists because CAPI v1beta2 turned kubeletExtraArgs from a
// map into a name-keyed list; without it the per-provider overlay's single
// provider-id entry would wipe the shared payload's kubelet arguments. Every
// other list still replaces: an overlay that adds one preKubeadmCommand
// replaces every command the shared payload carries, including the
// cross-cloud ones a spanning cluster needs. The rule is documented on the
// CRD fields so it shows up in `kubectl explain`.
//
// Merge patch rather than strategic merge patch is a deliberate trade. Strategic
// merge needs the target type's patchStrategy/patchMergeKey struct tags or its
// OpenAPI schema at runtime; we handle children as unstructured precisely to
// avoid vendoring two providers' Go type trees and pinning ourselves to their
// release cadence. RFC 7386 needs no schema at all. If element-wise list merging
// is ever genuinely required, the API grows a patchType discriminator and
// RFC 6902 covers list operations exactly.
//
// See docs/design.md for configuration and merge rules.
func MergePatch(base map[string]any, patch *apiextensionsv1.JSON) (map[string]any, error) {
	if patch == nil || len(patch.Raw) == 0 {
		return runtime.DeepCopyJSON(base), nil
	}

	overlay, err := payloadMap(patch)
	if err != nil {
		return nil, fmt.Errorf("MergePatch: parsing merge patch: %w", err)
	}

	return MergeBootstrapObjects(base, overlay), nil
}

// MergeBootstrapObjects composes decoded JSON objects with the documented named-list
// extension. Both inputs and the result are independent; json.Number values are retained.
func MergeBootstrapObjects(base, overlay map[string]any) map[string]any {
	return mergeInto(runtime.DeepCopyJSON(base), runtime.DeepCopyJSON(overlay), true)
}

// MergeInfrastructureObjects applies ordinary JSON Merge Patch semantics to native
// infrastructure. Arrays replace, even when their elements have name fields.
func MergeInfrastructureObjects(base, overlay map[string]any) map[string]any {
	return mergeInto(runtime.DeepCopyJSON(base), runtime.DeepCopyJSON(overlay), false)
}

// mergeInto applies overlay onto base, returning a new map. Neither input is
// mutated: the shared base payload is reused across every provider in a
// reconcile, and an in-place merge would let one provider's overlay leak into
// another's cascade.
func mergeInto(base, overlay map[string]any, namedLists bool) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	maps.Copy(out, base)

	for k, ov := range overlay {
		// RFC 7386: an explicit null removes the member.
		if ov == nil {
			delete(out, k)

			continue
		}

		overlayMap, overlayIsMap := ov.(map[string]any)
		baseMap, _ := out[k].(map[string]any)

		if overlayIsMap {
			// RFC 7386 treats a non-object target as an empty object, so
			// nested null members are deleted even on a newly added branch.
			out[k] = mergeInto(baseMap, overlayMap, namedLists)

			continue
		}

		if namedLists {
			if merged, ok := mergeNamedList(out[k], ov); ok {
				out[k] = merged
				continue
			}
		}

		// Scalars, arrays, and type changes all replace outright.
		out[k] = ov
	}

	return out
}

// mergeNamedList merges two name-keyed lists (every element an object with a
// string "name"): base order kept, the overlay's entry replacing the base's
// on a shared name, new names appended. Reports false when either side is
// not such a list, in which case the caller replaces.
func mergeNamedList(base, overlay any) ([]any, bool) {
	baseList, ok := namedList(base)
	if !ok {
		return nil, false
	}

	overlayList, ok := namedList(overlay)
	if !ok {
		return nil, false
	}

	index := make(map[string]int, len(baseList))
	out := make([]any, 0, len(baseList)+len(overlayList))

	for _, e := range baseList {
		name, _ := e["name"].(string)
		index[name] = len(out)
		out = append(out, e)
	}

	for _, e := range overlayList {
		name, _ := e["name"].(string)
		if i, dup := index[name]; dup {
			out[i] = e

			continue
		}

		index[name] = len(out)
		out = append(out, e)
	}

	return out, true
}

// namedList reports whether v is a non-empty list whose every element is an
// object with a string "name".
func namedList(v any) ([]map[string]any, bool) {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil, false
	}

	out := make([]map[string]any, 0, len(list))

	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, false
		}

		if _, ok := m["name"].(string); !ok {
			return nil, false
		}

		out = append(out, m)
	}

	return out, true
}
