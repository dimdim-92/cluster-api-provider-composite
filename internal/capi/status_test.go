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

package capi_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
)

func TestReadInfraCluster_ValidatesEndpointPort(t *testing.T) {
	for _, port := range []int64{-1, 65536, 1<<32 + 6443} {
		t.Run(fmt.Sprint(port), func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
				"controlPlaneEndpoint": map[string]any{"host": "api.example.test", "port": port},
			}}}
			_, err := capi.ReadInfraCluster(obj, capi.ContractV1Beta2)
			require.ErrorIs(t, err, capi.ErrInvalidEndpoint, "out-of-range ports must not be narrowed into a usable endpoint")
		})
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"controlPlaneEndpoint": map[string]any{"host": "api.example.test", "port": int64(65535)},
	}}}
	status, err := capi.ReadInfraCluster(obj, capi.ContractV1Beta2)
	require.NoError(t, err)
	assert.Equal(t, int32(65535), status.Endpoint.Port)
}

func TestReadInfraCluster_ContractConditionMatrix(t *testing.T) {
	t.Parallel()
	for _, contract := range []capi.ContractVersion{capi.ContractV1Beta1, capi.ContractV1Beta2} {
		for _, provisioned := range []bool{false, true} {
			for _, tc := range []struct {
				name       string
				status     string
				generation *int64
				ready      bool
				reason     string
			}{
				{"absent-ready-fallback", "", nil, provisioned, "LegacyFailure"},
				{"true-current", "True", new(int64(7)), true, "ChildHealth"},
				{"false-current", "False", new(int64(7)), false, "ChildHealth"},
				{"unknown-current", "Unknown", new(int64(7)), false, "ChildHealth"},
				{"true-no-generation", "True", nil, true, "ChildHealth"},
				{"false-no-generation", "False", nil, false, "ChildHealth"},
				{"unknown-no-generation", "Unknown", nil, false, "ChildHealth"},
				{"stale", "True", new(int64(6)), false, "StaleReadyCondition"},
				{"future", "True", new(int64(8)), false, "StaleReadyCondition"},
				{"explicit-zero", "True", new(int64(0)), false, "StaleReadyCondition"},
			} {
				t.Run(fmt.Sprintf("%s/provisioned=%t/%s", contract, provisioned, tc.name), func(t *testing.T) {
					t.Parallel()
					// Opposite initialization bits ensure the selected contract, not the API version, determines the path.
					legacy := provisioned
					if contract == capi.ContractV1Beta2 {
						legacy = !provisioned
					}
					obj := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{
						"ready": legacy, "initialization": map[string]any{"provisioned": !legacy},
						"failureReason": "LegacyFailure", "failureMessage": "legacy detail",
					}}}
					obj.SetAPIVersion("example.test/v1")
					obj.SetGeneration(7)
					// Unrelated conditions must neither override readiness nor need Ready's field types.
					conditions := []any{map[string]any{"type": "ProviderSpecific", "status": int64(42)}}
					if tc.status != "" {
						ready := map[string]any{
							"type": infrav1.ReadyCondition, "status": tc.status,
							"reason": "ChildHealth", "message": "child detail",
						}
						if tc.generation != nil {
							ready["observedGeneration"] = *tc.generation
						}
						conditions = append(conditions, ready)
					}
					require.NoError(t, unstructured.SetNestedSlice(obj.Object, conditions, "status", "conditions"))
					got, err := capi.ReadInfraCluster(obj, contract)
					require.NoError(t, err)
					assert.Equal(t, provisioned, got.Provisioned)
					assert.Equal(t, tc.ready, got.Ready)
					assert.Equal(t, tc.reason, got.FailureReason)
					assert.NotEmpty(t, got.FailureMessage)
				})
			}
		}
	}
}

func TestReadInfraCluster_MissingAndMalformedFields(t *testing.T) {
	t.Parallel()
	for _, contract := range []capi.ContractVersion{capi.ContractV1Beta1, capi.ContractV1Beta2} {
		t.Run(string(contract), func(t *testing.T) {
			t.Parallel()
			got, err := capi.ReadInfraCluster(&unstructured.Unstructured{Object: map[string]any{}}, contract)
			require.NoError(t, err)
			assert.Equal(t, capi.ChildStatus{}, got)
			for _, tc := range []struct {
				name   string
				status map[string]any
			}{
				{"provisioning", map[string]any{"ready": "yes", "initialization": map[string]any{"provisioned": "yes"}}},
				{"conditions-list", map[string]any{"conditions": "invalid"}},
				{"ready-status", map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": true}}}},
				{"ready-generation", map[string]any{"conditions": []any{map[string]any{"type": "Ready", "observedGeneration": "7"}}}},
				{"failure-reason", map[string]any{"failureReason": int64(1)}},
				{"failure-message", map[string]any{"failureMessage": true}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					_, err := capi.ReadInfraCluster(&unstructured.Unstructured{Object: map[string]any{"status": tc.status}}, contract)
					require.Error(t, err)
				})
			}
		})
	}
	_, err := capi.ReadInfraCluster(&unstructured.Unstructured{}, "unsupported")
	require.ErrorIs(t, err, capi.ErrUnknownContract)
}

func TestReadEndpoint_TypesAndPartialValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		endpoint map[string]any
		want     infrav1.APIEndpoint
		invalid  bool
	}{
		{"absent", nil, infrav1.APIEndpoint{}, false},
		{"host-only", map[string]any{"host": "api.example.test"}, infrav1.APIEndpoint{Host: "api.example.test"}, false},
		{"port-only", map[string]any{"port": int64(1)}, infrav1.APIEndpoint{Port: 1}, false},
		{"zero", map[string]any{"port": int64(0)}, infrav1.APIEndpoint{}, false},
		{"max", map[string]any{"port": int64(65535)}, infrav1.APIEndpoint{Port: 65535}, false},
		{"string", map[string]any{"port": "6443"}, infrav1.APIEndpoint{}, true},
		{"float", map[string]any{"port": float64(6443)}, infrav1.APIEndpoint{}, true},
		{"fractional", map[string]any{"port": 6443.5}, infrav1.APIEndpoint{}, true},
		{"bool", map[string]any{"port": true}, infrav1.APIEndpoint{}, true},
		{"host-type", map[string]any{"host": int64(42)}, infrav1.APIEndpoint{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}
			if tc.endpoint != nil {
				require.NoError(t, unstructured.SetNestedMap(obj.Object, tc.endpoint, "spec", "controlPlaneEndpoint"))
			}
			got, err := capi.ReadEndpoint(obj)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestReadInfraCluster_ReadyWithoutDiagnostics(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{
		"ready": true, "failureReason": "LegacyFailure", "failureMessage": "legacy detail",
		"conditions": []any{map[string]any{"type": "Ready", "status": string(metav1.ConditionFalse)}},
	}}}
	got, err := capi.ReadInfraCluster(obj, capi.ContractV1Beta1)
	require.NoError(t, err)
	assert.True(t, got.Provisioned)
	assert.False(t, got.Ready, "an explicit failure still overrides fallback without a reason/message")
	assert.Equal(t, "LegacyFailure", got.FailureReason)
	assert.Equal(t, "legacy detail", got.FailureMessage)
}
