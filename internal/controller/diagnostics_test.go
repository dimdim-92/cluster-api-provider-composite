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

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/numspot/cluster-api-provider-composite/internal/capi"
	"github.com/numspot/cluster-api-provider-composite/internal/profile"
)

func TestDiagnosticMessage(t *testing.T) {
	t.Parallel()
	const (
		secret    = "diagnostic-secret-canary"
		ownership = "Child ownership is unsafe; check controller owner references and field ownership before retrying."
		contract  = "Provider API contract is invalid or unsupported; check CRD contract labels, served versions and namespaced scope."
		payload   = "Payload rendering or schema validation failed; check provider and bootstrap configuration against the target schema."
		timeout   = "API request timed out; check API availability and connectivity, then retry reconciliation."
		fallback  = "Reconciliation failed; check controller configuration and API availability."
	)
	detail := strings.Repeat(secret, 128)
	resource := schema.GroupResource{Group: detail, Resource: detail}
	invalid := apierrors.NewInvalid(schema.GroupKind{Group: detail, Kind: detail}, detail, field.ErrorList{
		field.Invalid(field.NewPath(detail), detail, detail),
	})
	assert.Contains(t, invalid.ErrStatus.Message, secret)
	require.Len(t, invalid.ErrStatus.Details.Causes, 1)
	assert.Contains(t, invalid.ErrStatus.Details.Causes[0].Message, secret)
	var decoded map[string]any
	syntaxErr := json.Unmarshal([]byte(`{"password":"`+detail+`","payload":`), &decoded)
	require.Error(t, syntaxErr)
	assert.Empty(t, diagnosticMessage(nil))
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{"child ownership", capi.ErrChildNotOwned, ownership},
		{"missing owner", capi.ErrNoControllerOwner, ownership},
		{"field ownership", capi.ErrFieldOwnership, ownership},
		{
			"invalid composition", errInvalidComposition,
			"Provider composition is invalid; check append-only bindings, distinct child identities and canonical infrastructure names.",
		},
		{
			"forbidden", apierrors.NewForbidden(resource, detail, errors.New(detail)),
			"API access is forbidden; check the controller service account's RBAC permissions for the target resource and namespace.",
		},
		{"unauthorized", apierrors.NewUnauthorized(detail), "API authentication failed; check the controller's credentials."},
		{
			"conflict", apierrors.NewConflict(resource, detail, errors.New(detail)),
			"API update conflicted; retry reconciliation against the latest resource version.",
		},
		{"not installed", capi.ErrCRDNotInstalled, "Provider API is not installed; install the required provider CRDs."},
		{"invalid credential reference", profile.ErrInvalidCredentials, "Profile credential reference is invalid; check its Secret name and native target paths."},
		{"missing contract label", capi.ErrNoContractLabel, contract},
		{"invalid contract", capi.ErrInvalidContract, contract},
		{"unknown contract", capi.ErrUnknownContract, contract},
		{"invalid payload", invalid, payload},
		{"bad request", apierrors.NewBadRequest(detail), payload},
		{"typed patch", apierrors.NewInternalError(errors.New("failed to create typed patch object: " + detail)), payload},
		{"render syntax", syntaxErr, payload},
		{"render type", &json.UnmarshalTypeError{Value: detail, Type: reflect.TypeFor[string](), Struct: detail, Field: detail}, payload},
		{"deadline", context.DeadlineExceeded, timeout},
		{"API timeout", apierrors.NewTimeoutError(detail, 1), timeout},
		{"server timeout", apierrors.NewServerTimeout(resource, detail, 1), timeout},
		{"transport timeout", &url.Error{Op: detail, URL: "https://example.invalid/?token=" + detail, Err: os.ErrDeadlineExceeded}, timeout},
		{"canceled", context.Canceled, "Reconciliation was canceled; retry when the controller context is active."},
		{"transport failure", &url.Error{Op: detail, URL: "https://example.invalid/?token=" + detail, Err: errors.New(detail)}, fallback},
		{"custom error", &diagnosticTestError{message: detail}, fallback},
		{"custom wrapped status", &diagnosticTestError{message: detail, cause: invalid}, payload},
		{"server failure", apierrors.NewInternalError(errors.New(detail)), fallback},
		{"arbitrary child message", errors.New("Forbidden: failed to create typed patch object: " + detail), fallback},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, err := range []error{tt.err, fmt.Errorf("%s: %w", detail, tt.err), errors.Join(errors.New(detail), tt.err)} {
				message := diagnosticMessage(err)
				assert.Equal(t, tt.want, message)
				assert.Less(t, len(message), 1024)
				assert.NotContains(t, message, secret)
			}
		})
	}
}

func TestSafeReconcileError(t *testing.T) {
	t.Parallel()
	const secret = "safe-reconcile-secret-canary"
	detail := strings.Repeat(secret, 128)
	status := &apierrors.StatusError{ErrStatus: metav1.Status{
		Reason: metav1.StatusReasonInvalid, Code: 422, Message: detail,
		Details: &metav1.StatusDetails{
			Name: detail, Group: detail, Kind: detail,
			Causes: []metav1.StatusCause{{Type: metav1.CauseTypeFieldValueInvalid, Field: detail, Message: "Invalid value: " + detail}},
		},
	}}
	transport := &url.Error{Op: detail, URL: "https://user:" + detail + "@example.invalid/?token=" + detail, Err: context.DeadlineExceeded}
	custom := &diagnosticTestError{message: detail, cause: capi.ErrChildNotOwned}
	joined := errors.Join(errors.New(detail), status, errors.Join(transport, custom))
	require.Nil(t, safeReconcileError(nil))
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"status", status},
		{"transport", transport},
		{"custom", custom},
		{"joined", joined},
		{"wrapped join", fmt.Errorf("%s: %w", detail, joined)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := safeReconcileError(tt.err)
			require.NotNil(t, got)
			assert.Same(t, tt.err, errors.Unwrap(got))
			assert.ErrorIs(t, got, tt.err)
			for _, message := range []string{got.Error(), fmt.Sprint(got), fmt.Sprintf("%+v", got), safeReconcileError(got).Error()} {
				assert.Equal(t, diagnosticMessage(tt.err), message)
				assert.Less(t, len(message), 1024)
				assert.NotContains(t, message, secret)
			}
			for _, cause := range []error{status, transport, custom, context.DeadlineExceeded, capi.ErrChildNotOwned} {
				assert.Equal(t, errors.Is(tt.err, cause), errors.Is(got, cause))
			}
			if original, ok := errors.AsType[*apierrors.StatusError](tt.err); ok {
				var preserved *apierrors.StatusError
				require.ErrorAs(t, got, &preserved)
				assert.Same(t, original, preserved)
			}
			if original, ok := errors.AsType[*url.Error](tt.err); ok {
				var preserved *url.Error
				require.ErrorAs(t, got, &preserved)
				assert.Same(t, original, preserved)
			}
			if original, ok := errors.AsType[*diagnosticTestError](tt.err); ok {
				var preserved *diagnosticTestError
				require.ErrorAs(t, got, &preserved)
				assert.Same(t, original, preserved)
			}
		})
	}
}

type diagnosticTestError struct {
	message string
	cause   error
}

func (e *diagnosticTestError) Error() string {
	return e.message
}

func (e *diagnosticTestError) Unwrap() error {
	return e.cause
}
