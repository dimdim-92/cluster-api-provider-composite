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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/profile"
)

func raw(s string) *apiextensionsv1.JSON { return &apiextensionsv1.JSON{Raw: []byte(s)} }

func exampleProfile() *profile.Configuration {
	return &profile.Configuration{
		Defaults: &profile.Defaults{
			InfraSpec: raw(
				`{"region":"r1","identityRef":{"name":"fixed"},"network":{"cidr":"old","private":true},"servers":[{"name":"a","value":"old"},{"name":"b"}],"big":9007199254740993}`,
			),
			Bootstrap: raw(
				`{"preKubeadmCommands":["profile"],"joinConfiguration":{"nodeRegistration":{"kubeletExtraArgs":[{"name":"provider-id","value":"native"}]}}}`,
			),
		},
		OverridePolicy: &infrav1.CompositeProviderOverridePolicy{
			InfraSpecPaths:          []infrav1.ProfileJSONPointer{"/network", "/servers"},
			BootstrapPaths:          []infrav1.ProfileJSONPointer{"/files", "/preKubeadmCommands", "/joinConfiguration"},
			ProtectedInfraSpecPaths: []infrav1.ProfileJSONPointer{"/region", "/identityRef", "/network/private"},
		},
	}
}

func TestResolve_InvalidInputsAndPolicy(t *testing.T) {
	for _, pointer := range []infrav1.ProfileJSONPointer{"", "/", "/bad~2escape", "/bad~", "/wild/*", "/empty//part"} {
		t.Run(string(pointer), func(t *testing.T) {
			spec := exampleProfile()
			spec.OverridePolicy.InfraSpecPaths = []infrav1.ProfileJSONPointer{pointer}
			_, err := profile.Resolve(spec, nil, nil, nil)
			require.ErrorIs(t, err, profile.ErrInvalidPolicy)
		})
	}
	for _, malformed := range []string{`null`, `[]`, `{"a":`, `{} {}`} {
		_, err := profile.Resolve(exampleProfile(), nil, raw(malformed), nil)
		require.ErrorIs(t, err, profile.ErrInvalidJSON)
	}
	spec := exampleProfile()
	spec.OverridePolicy.ProtectedInfraSpecPaths = append(spec.OverridePolicy.ProtectedInfraSpecPaths, "/controlPlaneEndpoint")
	_, err := profile.Resolve(spec, nil, nil, nil)
	require.NoError(t, err, "explicit endpoint protection and implicit controller protection may agree")
	spec.OverridePolicy.InfraSpecPaths = append(spec.OverridePolicy.InfraSpecPaths, spec.OverridePolicy.InfraSpecPaths[0])
	_, err = profile.Resolve(spec, nil, nil, nil)
	require.ErrorIs(t, err, profile.ErrInvalidPolicy)
}

func TestResolve_BootstrapAndAbsentProtection(t *testing.T) {
	spec := exampleProfile()
	spec.OverridePolicy.ProtectedBootstrapPaths = []infrav1.ProfileJSONPointer{"/joinConfiguration"}
	_, err := profile.Resolve(spec, raw(`{"joinConfiguration":{"injected":true}}`), nil, nil)
	require.ErrorIs(t, err, profile.ErrProtectedOverride, "shared bootstrap cannot bypass protection")
	spec.OverridePolicy.InfraSpecPaths = append(spec.OverridePolicy.InfraSpecPaths, "/credentials")
	spec.OverridePolicy.ProtectedInfraSpecPaths = append(spec.OverridePolicy.ProtectedInfraSpecPaths, "/credentials")
	_, err = profile.Resolve(spec, nil, raw(`{"credentials":"SECRET"}`), nil)
	require.ErrorIs(t, err, profile.ErrProtectedOverride)
	assert.NotContains(t, err.Error(), "SECRET")
	spec = exampleProfile()
	resolved, err := profile.Resolve(spec, nil, raw(`{"network":{"cidr":null}}`), nil)
	require.NoError(t, err)
	network, ok := resolved.InfraSpec["network"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, network, "cidr")
	assert.Equal(t, true, network["private"])
}

func TestResolve_SharedBootstrapNullsAreDeletes(t *testing.T) {
	shared := raw(`{"files":null,"joinConfiguration":{"nodeRegistration":{"name":null}}}`)
	before := string(shared.Raw)
	resolved, err := profile.Resolve(exampleProfile(), shared, nil, nil)
	require.NoError(t, err)
	assert.NotContains(t, resolved.Bootstrap, "files")
	join, ok := resolved.Bootstrap["joinConfiguration"].(map[string]any)
	require.True(t, ok)
	registration, ok := join["nodeRegistration"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, registration, "name")
	assert.NotEmpty(t, registration["kubeletExtraArgs"], "source defaults still apply after shared values")
	assert.Equal(t, before, string(shared.Raw), "resolution must not mutate shared input")
}

func TestResolve_MergeOrderAndIsolation(t *testing.T) {
	spec := exampleProfile()
	before, err := json.Marshal(spec)
	require.NoError(t, err)
	shared := raw(
		`{"preKubeadmCommands":["shared"],"files":[{"path":"/shared"}],"joinConfiguration":{"nodeRegistration":{"kubeletExtraArgs":[{"name":"cloud-provider","value":"external"}]}}}`,
	)
	infra := raw(`{"network":{"cidr":"new"},"servers":[{"name":"a","value":"new"}]}`)
	bootstrap := raw(`{"files":null,"joinConfiguration":{"nodeRegistration":{"kubeletExtraArgs":[{"name":"custom","value":"yes"}]}}}`)
	resolved, err := profile.Resolve(spec, shared, infra, bootstrap)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]any{map[string]any{"name": "a", "value": "new"}},
		resolved.InfraSpec["servers"],
		"native arrays replace, without bootstrap's named-list extension",
	)
	assert.Equal(t, json.Number("9007199254740993"), resolved.InfraSpec["big"])
	assert.Equal(t, []any{"profile"}, resolved.Bootstrap["preKubeadmCommands"])
	assert.NotContains(t, resolved.Bootstrap, "files")
	encoded, err := json.Marshal(resolved.Bootstrap)
	require.NoError(t, err)
	assert.JSONEq(
		t,
		`{"preKubeadmCommands":["profile"],"joinConfiguration":{"nodeRegistration":{"kubeletExtraArgs":[{"name":"cloud-provider","value":"external"},{"name":"provider-id","value":"native"},{"name":"custom","value":"yes"}]}}}`,
		string(encoded),
	)
	network, ok := resolved.InfraSpec["network"].(map[string]any)
	require.True(t, ok)
	network["cidr"] = "mutated"
	after, err := json.Marshal(spec)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestResolve_ProtectsIdentityAndAllowedBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, patch string
		want        error
	}{
		{"region", `{"region":"different"}`, profile.ErrProtectedOverride},
		{"identity", `{"identityRef":{"name":"different"}}`, profile.ErrProtectedOverride},
		{"parent-delete", `{"network":null}`, profile.ErrProtectedOverride},
		{"parent-replace", `{"network":"replacement"}`, profile.ErrProtectedOverride},
		{"prefix-confusion", `{"networkExtra":true}`, profile.ErrOverrideDenied},
		{"endpoint", `{"controlPlaneEndpoint":{"host":"attacker","port":443}}`, profile.ErrProtectedOverride},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := exampleProfile()
			spec.OverridePolicy.InfraSpecPaths = append(spec.OverridePolicy.InfraSpecPaths, "/controlPlaneEndpoint")
			result, err := profile.Resolve(spec, nil, raw(tc.patch), nil)
			require.ErrorIs(t, err, tc.want)
			assert.Nil(t, result)
		})
	}
	spec := exampleProfile()
	spec.OverridePolicy = nil
	_, err := profile.Resolve(spec, raw(`{"files":[{"content":"SECRET-CANARY"}]}`), nil, nil)
	require.ErrorIs(t, err, profile.ErrOverrideDenied)
	assert.NotContains(t, err.Error(), "SECRET-CANARY")
	spec = exampleProfile()
	spec.OverridePolicy.InfraSpecPaths = []infrav1.ProfileJSONPointer{"/servers/0"}
	_, err = profile.Resolve(spec, nil, nil, nil)
	require.ErrorIs(t, err, profile.ErrInvalidPolicy)
}

func TestResolve_EscapesAndNewSubtrees(t *testing.T) {
	spec := exampleProfile()
	spec.OverridePolicy.InfraSpecPaths = []infrav1.ProfileJSONPointer{"/a~1b", "/new/allowed"}
	resolved, err := profile.Resolve(spec, nil, raw(`{"a/b":"yes","new":{"allowed":true}}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "yes", resolved.InfraSpec["a/b"])
	_, err = profile.Resolve(spec, nil, raw(`{"new":{"denied":true}}`), nil)
	require.ErrorIs(t, err, profile.ErrOverrideDenied)
}
