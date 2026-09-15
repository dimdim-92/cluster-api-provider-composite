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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/profile"
)

func TestCredentialsRef_Resolution(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
		paths              []infrav1.ProfileJSONPointer
	}{
		{
			"outscale", `{"credentials":{"fromSecret":"source-default"},"big":9007199254740993}`,
			`{"credentials":{"fromSecret":"regional-credentials"},"big":9007199254740993}`,
			[]infrav1.ProfileJSONPointer{"/credentials/fromSecret"},
		},
		{
			"openstack", `{"identityRef":{"name":"source-default","cloudName":"openstack","region":"region-a"}}`,
			`{"identityRef":{"name":"regional-credentials","cloudName":"openstack","region":"region-a"}}`,
			[]infrav1.ProfileJSONPointer{"/identityRef/name"},
		},
		{
			"missing-properties", `{}`, `{"auth":{"secretName":"regional-credentials"}}`,
			[]infrav1.ProfileJSONPointer{"/auth/secretName"},
		},
		{
			"null-leaf", `{"auth":{"secretName":null}}`, `{"auth":{"secretName":"regional-credentials"}}`,
			[]infrav1.ProfileJSONPointer{"/auth/secretName"},
		},
		{
			"escaped-and-multiple", `{}`, `{"a/b":{"~secret":"regional-credentials"},"other":{"name":"regional-credentials"}}`,
			[]infrav1.ProfileJSONPointer{"/a~1b/~0secret", "/other/name"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configuration := &profile.Configuration{
				Defaults:       &profile.Defaults{InfraSpec: raw(tc.source)},
				CredentialsRef: infrav1.ProviderCredentialsReference{Name: "regional-credentials", NamePaths: tc.paths},
			}
			before, err := json.Marshal(configuration)
			require.NoError(t, err)
			resolved, err := profile.Resolve(configuration, nil, nil, nil)
			require.NoError(t, err)
			encoded, err := json.Marshal(resolved.InfraSpec)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(encoded))
			if tc.name == "outscale" {
				assert.Equal(t, json.Number("9007199254740993"), resolved.InfraSpec["big"])
			}
			for key := range resolved.InfraSpec {
				delete(resolved.InfraSpec, key)
			}
			after, err := json.Marshal(configuration)
			require.NoError(t, err)
			assert.Equal(t, before, after, "reference injection must not mutate source payloads or policy")
		})
	}
}

func TestCredentialsRef_ProtectedOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, patch string
		want        error
	}{
		{"no-change", `{}`, nil},
		{"same-reference", `{"identityRef":{"name":"regional-credentials"}}`, nil},
		{"unprotected-sibling", `{"identityRef":{"cloudName":"other-cloud"}}`, nil},
		{"direct-change", `{"identityRef":{"name":"PRIVATE-CANARY"}}`, profile.ErrProtectedOverride},
		{"delete-reference", `{"identityRef":{"name":null}}`, profile.ErrProtectedOverride},
		{"delete-parent", `{"identityRef":null}`, profile.ErrProtectedOverride},
		{"replace-parent", `{"identityRef":"PRIVATE-CANARY"}`, profile.ErrProtectedOverride},
		{"array-parent", `{"identityRef":[{"name":"regional-credentials"}]}`, profile.ErrProtectedOverride},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configuration := &profile.Configuration{
				Defaults: &profile.Defaults{InfraSpec: raw(`{"identityRef":{"name":"source-default"}}`)},
				CredentialsRef: infrav1.ProviderCredentialsReference{
					Name:      "regional-credentials",
					NamePaths: []infrav1.ProfileJSONPointer{"/identityRef/name"},
				},
				OverridePolicy: &infrav1.CompositeProviderOverridePolicy{InfraSpecPaths: []infrav1.ProfileJSONPointer{"/identityRef"}},
			}
			resolved, err := profile.Resolve(configuration, nil, raw(tc.patch), nil)
			require.ErrorIs(t, err, tc.want)
			if tc.want != nil {
				assert.Nil(t, resolved)
				assert.NotContains(t, err.Error(), "PRIVATE-CANARY")
			}
			configuration.OverridePolicy.ProtectedInfraSpecPaths = configuration.CredentialsRef.NamePaths
			_, err = profile.Resolve(configuration, nil, raw(tc.patch), nil)
			require.ErrorIs(t, err, tc.want, "explicit and implicit protection may agree")
		})
	}
}

func TestCredentialsRef_InvalidReference(t *testing.T) {
	for _, tc := range []struct {
		name, secret, source string
		paths                []infrav1.ProfileJSONPointer
		endpoints            []string
	}{
		{name: "empty-name", paths: []infrav1.ProfileJSONPointer{"/auth/name"}},
		{name: "invalid-name", secret: "Bad_Name", paths: []infrav1.ProfileJSONPointer{"/auth/name"}},
		{name: "namespaced-name", secret: "other/secret", paths: []infrav1.ProfileJSONPointer{"/auth/name"}},
		{name: "long-name", secret: strings.Repeat("a", 254), paths: []infrav1.ProfileJSONPointer{"/auth/name"}},
		{name: "missing-paths", secret: "regional-credentials"},
		{name: "root", paths: []infrav1.ProfileJSONPointer{"/"}},
		{name: "invalid-escape", paths: []infrav1.ProfileJSONPointer{"/a~2b"}},
		{name: "wildcard", paths: []infrav1.ProfileJSONPointer{"/auth/*"}},
		{name: "long-path", paths: []infrav1.ProfileJSONPointer{infrav1.ProfileJSONPointer("/" + strings.Repeat("a", 512))}},
		{name: "duplicate-paths", paths: []infrav1.ProfileJSONPointer{"/auth/name", "/auth/name"}},
		{name: "overlapping-paths", paths: []infrav1.ProfileJSONPointer{"/auth", "/auth/name"}},
		{name: "overlapping-reversed", paths: []infrav1.ProfileJSONPointer{"/auth/name", "/auth"}},
		{name: "too-many-paths", paths: []infrav1.ProfileJSONPointer{"/a", "/b", "/c", "/d", "/e", "/f", "/g", "/h", "/i"}},
		{name: "array-traversal", source: `{"auth":[{}]}`, paths: []infrav1.ProfileJSONPointer{"/auth/0/name"}},
		{name: "scalar-traversal", source: `{"auth":"PRIVATE-CANARY"}`, paths: []infrav1.ProfileJSONPointer{"/auth/name"}},
		{name: "object-leaf", source: `{"auth":{}}`, paths: []infrav1.ProfileJSONPointer{"/auth"}},
		{name: "array-leaf", source: `{"auth":[]}`, paths: []infrav1.ProfileJSONPointer{"/auth"}},
		{name: "number-leaf", source: `{"auth":1}`, paths: []infrav1.ProfileJSONPointer{"/auth"}},
		{name: "default-endpoint", paths: []infrav1.ProfileJSONPointer{"/controlPlaneEndpoint/host"}},
		{name: "custom-endpoint", paths: []infrav1.ProfileJSONPointer{"/auth/name"}, endpoints: []string{"spec.auth.name"}},
		{name: "endpoint-ancestor", paths: []infrav1.ProfileJSONPointer{"/auth"}, endpoints: []string{"spec.auth.endpoint"}},
		{name: "endpoint-descendant", paths: []infrav1.ProfileJSONPointer{"/auth/name"}, endpoints: []string{"spec.auth"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.secret == "" && tc.name != "empty-name" {
				tc.secret = "regional-credentials"
			}
			if tc.source == "" {
				tc.source = `{}`
			}
			configuration := &profile.Configuration{
				Defaults:       &profile.Defaults{InfraSpec: raw(tc.source)},
				CredentialsRef: infrav1.ProviderCredentialsReference{Name: tc.secret, NamePaths: tc.paths},
				EndpointPaths:  tc.endpoints,
			}
			resolved, err := profile.Resolve(configuration, nil, nil, nil)
			require.ErrorIs(t, err, profile.ErrInvalidCredentials)
			assert.Nil(t, resolved)
			assert.NotContains(t, err.Error(), "PRIVATE-CANARY")
		})
	}
}

func TestCredentialsRef_LegacyAndPathBoundaries(t *testing.T) {
	configuration := &profile.Configuration{Defaults: &profile.Defaults{InfraSpec: raw(`{"credentials":{"fromSecret":"legacy"}}`)}}
	resolved, err := profile.Resolve(configuration, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"fromSecret": "legacy"}, resolved.InfraSpec["credentials"])
	configuration.CredentialsRef = infrav1.ProviderCredentialsReference{
		Name: "regional-credentials", NamePaths: []infrav1.ProfileJSONPointer{"/a", "/b", "/c", "/d", "/e", "/f", "/g", "/h"},
	}
	_, err = profile.Resolve(configuration, nil, nil, nil)
	require.NoError(t, err)
	configuration.CredentialsRef.NamePaths = []infrav1.ProfileJSONPointer{"/controlPlaneEndpointExtra/name", "/auth/0/name"}
	_, err = profile.Resolve(configuration, nil, nil, nil)
	require.NoError(t, err, "object keys are not array selectors; endpoint prefixes are compared by segments")
}
