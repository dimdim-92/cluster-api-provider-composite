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

package capi

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DryRunChild server-side dry-run applies a rendered child, so the target
// CRD's own schema validates the opaque payload before anything is persisted
// (schema enforcement comes from the provider's CRD, which is what
// keeps validation provider-agnostic). The returned error is the API server's
// verdict, field paths included; nil means the real apply will not be
// rejected on schema grounds.
//
// What this catches: type mismatches, missing required fields, CEL and
// format violations — the ways a platform-authored payload can actually go
// wrong. What it cannot catch: unknown fields, which structural schemas PRUNE
// silently rather than reject. A typo'd optional key vanishes from the
// persisted child instead of erroring; that is Kubernetes semantics, not a
// gap this layer can close.
//
// The object is deep-copied first: the API server returns the defaulted
// object into its argument, and rendered children must stay exactly what the
// mint produced.
func DryRunChild(ctx context.Context, c client.Client, obj *unstructured.Unstructured) error {
	err := c.Apply(ctx, client.ApplyConfigurationFromUnstructured(obj.DeepCopy()), FieldOwner, client.ForceOwnership, client.DryRunAll)
	if err != nil {
		// %w, so IsPayloadRejection still sees the *StatusError underneath.
		return fmt.Errorf("DryRunChild: %s %q: %w", obj.GetKind(), obj.GetName(), err)
	}

	return nil
}

// typedPatchMarker is how the API server reports a server-side-apply patch
// that does not fit the target schema's TYPES (e.g. a string field given a
// number). SSA converts the patch to a typed object before validation ever
// runs, and that conversion failure surfaces as a 500 carrying this message —
// not as 422 Invalid. Verified against a real API server in
// tests/envtest/dryrun_test.go, which fails if this behaviour ever changes.
const typedPatchMarker = "failed to create typed patch object"

// IsPayloadRejection reports whether a DryRunChild error is the payload's
// fault, as opposed to a transport or server problem the reconciler should
// retry. Three shapes qualify, and all occur in practice: schema violations
// the validator catches (required fields, bounds, CEL) come back 422 Invalid;
// a malformed request is 400 BadRequest; and a TYPE mismatch dies inside
// SSA's typed-patch conversion as a 500 with typedPatchMarker. Branching on
// IsInvalid alone silently turns every mistyped payload field into an
// infinite retry.
func IsPayloadRejection(err error) bool {
	if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
		return true
	}

	return apierrors.IsInternalError(err) && strings.Contains(err.Error(), typedPatchMarker)
}
