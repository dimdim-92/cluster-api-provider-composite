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

package profile_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/profile"
)

func TestProfile_NilConfiguration(t *testing.T) {
	resolved, err := profile.Resolve(nil, nil, nil, nil)
	require.ErrorIs(t, err, profile.ErrInvalidProfile)
	assert.Nil(t, resolved)
}

func TestResolve_EndpointPaths(t *testing.T) {
	for name, paths := range map[string][]string{
		"non-spec":          {"status.endpoint"},
		"empty-property":    {"spec."},
		"empty-segment":     {"spec.a..endpoint"},
		"pointer-separator": {"spec.a/endpoint"},
		"pointer-escape":    {"spec.a~1endpoint"},
		"hyphen":            {"spec.api-endpoint"},
		"unicode":           {"spec.é"},
		"trailing-newline":  {"spec.endpoint\n"},
		"too-long":          {"spec." + strings.Repeat("a", 508)},
		"too-many":          {"spec.a", "spec.b", "spec.c", "spec.d", "spec.e", "spec.f", "spec.g", "spec.h", "spec.i"},
	} {
		t.Run(name, func(t *testing.T) {
			spec := exampleProfile()
			spec.EndpointPaths = paths
			resolved, err := profile.Resolve(spec, nil, nil, nil)
			require.ErrorIs(t, err, profile.ErrInvalidPolicy)
			assert.Nil(t, resolved)
		})
	}
	t.Run("custom-path", func(t *testing.T) {
		spec := exampleProfile()
		spec.EndpointPaths = []string{"spec.network.endpoint"}
		_, err := profile.Resolve(spec, nil, nil, nil)
		require.NoError(t, err)
		resolved, err := profile.Resolve(spec, nil, raw(`{"network":{"endpoint":{"host":"injected","port":443}}}`), nil)
		require.ErrorIs(t, err, profile.ErrProtectedOverride)
		assert.Nil(t, resolved)
	})
}

func TestResolve_PolicyBounds(t *testing.T) {
	for field := range 4 {
		t.Run(fmt.Sprint(field), func(t *testing.T) {
			spec := exampleProfile()
			p := spec.OverridePolicy
			paths := []*[]infrav1.ProfileJSONPointer{&p.InfraSpecPaths, &p.BootstrapPaths, &p.ProtectedInfraSpecPaths, &p.ProtectedBootstrapPaths}[field]
			*paths = make([]infrav1.ProfileJSONPointer, 32)
			for i := range *paths {
				(*paths)[i] = infrav1.ProfileJSONPointer(fmt.Sprintf("/property-%d", i))
			}
			_, err := profile.Resolve(spec, nil, nil, nil)
			require.NoError(t, err)
			*paths = append(*paths, "/extra")
			_, err = profile.Resolve(spec, nil, nil, nil)
			require.ErrorIs(t, err, profile.ErrInvalidPolicy)
		})
	}
	for _, tc := range []struct {
		name, pointer string
		want          error
	}{
		{"ascii-limit", "/" + strings.Repeat("a", 511), nil},
		{"unicode-limit", "/" + strings.Repeat("é", 511), nil},
		{"over-limit", "/" + strings.Repeat("a", 512), profile.ErrInvalidPolicy},
		{"invalid-utf8", "/\xff", profile.ErrInvalidPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := exampleProfile()
			spec.OverridePolicy.InfraSpecPaths = []infrav1.ProfileJSONPointer{infrav1.ProfileJSONPointer(tc.pointer)}
			_, err := profile.Resolve(spec, nil, nil, nil)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestResolve_ArrayTraversal(t *testing.T) {
	for _, tc := range []struct {
		name, allowed, protected, patch string
	}{
		{"allowed-before", "/servers/0", "/region", `{}`},
		{"protected-before", "/servers", "/servers/0", `{}`},
		{"allowed-after", "/new/0", "/region", `{"new":[{}]}`},
		{"protected-after", "/new", "/new/credential", `{"new":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := exampleProfile()
			spec.OverridePolicy.InfraSpecPaths = []infrav1.ProfileJSONPointer{infrav1.ProfileJSONPointer(tc.allowed)}
			spec.OverridePolicy.ProtectedInfraSpecPaths = []infrav1.ProfileJSONPointer{infrav1.ProfileJSONPointer(tc.protected)}
			resolved, err := profile.Resolve(spec, nil, raw(tc.patch), nil)
			require.ErrorIs(t, err, profile.ErrInvalidPolicy)
			assert.Nil(t, resolved)
		})
	}
	t.Run("literal-object-keys", func(t *testing.T) {
		spec := exampleProfile()
		spec.OverridePolicy.InfraSpecPaths = []infrav1.ProfileJSONPointer{"/new/0", "/a~01"}
		resolved, err := profile.Resolve(spec, nil, raw(`{"new":{"0":true},"a~1":"escaped"}`), nil)
		require.NoError(t, err)
		assert.Equal(t, "escaped", resolved.InfraSpec["a~1"])
		assert.Equal(t, map[string]any{"0": true}, resolved.InfraSpec["new"])
	})
}

func TestResolve_InvalidJSONLayers(t *testing.T) {
	for layer := range 5 {
		t.Run(fmt.Sprint(layer), func(t *testing.T) {
			spec := exampleProfile()
			var shared, infra, bootstrap *apiextensionsv1.JSON
			inputs := []**apiextensionsv1.JSON{&spec.Defaults.InfraSpec, &spec.Defaults.Bootstrap, &shared, &infra, &bootstrap}
			*inputs[layer] = raw(`{"SECRET-CANARY":`)
			resolved, err := profile.Resolve(spec, shared, infra, bootstrap)
			require.ErrorIs(t, err, profile.ErrInvalidJSON)
			assert.Nil(t, resolved)
			assert.NotContains(t, err.Error(), "SECRET-CANARY")
		})
	}
	t.Run("no-defaults", func(t *testing.T) {
		spec := exampleProfile()
		spec.Defaults, spec.OverridePolicy = nil, nil
		resolved, err := profile.Resolve(spec, nil, nil, nil)
		require.NoError(t, err)
		assert.Empty(t, resolved.InfraSpec)
		assert.Empty(t, resolved.Bootstrap)
	})
}

func TestResolve_ProtectedValueEquality(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		want                error
	}{
		{"equivalent-numbers", `9007199254740993`, `9007199254740993.0`, nil},
		{"equivalent-exponents", `1e1000000`, `10e999999`, nil},
		{"equivalent-decimals", `0.01e2`, `1.0`, nil},
		{"equivalent-negative-zero", `-0e99`, `0`, nil},
		{"distinct-large-numbers", `9007199254740993`, `9007199254740992`, profile.ErrProtectedOverride},
		{"array-order", `[1,2]`, `[2,1]`, profile.ErrProtectedOverride},
		{"equivalent-array", `[1,{"a":-0}]`, `[1.0,{"a":0}]`, nil},
		{"array-length", `[1]`, `[1,2]`, profile.ErrProtectedOverride},
		{"number-string", `1`, `"1"`, profile.ErrProtectedOverride},
		{"number-null", `1`, `null`, profile.ErrProtectedOverride},
		{"null-removal", `null`, `null`, profile.ErrProtectedOverride},
		{"object-member", `{"a":true}`, `{"b":true}`, profile.ErrProtectedOverride},
		{"empty-object", `{}`, `[]`, profile.ErrProtectedOverride},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := exampleProfile()
			spec.Defaults.InfraSpec = raw(`{"protected":` + tc.before + `}`)
			spec.OverridePolicy.ProtectedInfraSpecPaths = []infrav1.ProfileJSONPointer{"/protected"}
			resolved, err := profile.Resolve(spec, nil, raw(`{"protected":`+tc.after+`}`), nil)
			require.ErrorIs(t, err, tc.want)
			if tc.want != nil {
				assert.Nil(t, resolved)
			}
		})
	}
}
