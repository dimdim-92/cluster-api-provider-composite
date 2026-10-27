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

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeModuleRelativeGosecFixture(t *testing.T) {
	// gosec v2.29 emits locations relative to the scanned module, with no base URI.
	fixture := `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"gosec","informationUri":"https://example.test/gosec"}},"results":[{
		"ruleId":"G304","properties":{"exact":9007199254740993},
		"locations":[{"physicalLocation":{"artifactLocation":{"uri":"v1beta2/common_types.go"},"region":{"startLine":1}}}],
		"relatedLocations":[{"physicalLocation":{"artifactLocation":{"uri":"api/nested.go"}}}],
		"codeFlows":[{"threadFlows":[{"locations":[{"location":{"physicalLocation":{"artifactLocation":{"uri":"v1beta2/path%20with%20spaces.go"}}}}]}]}]
	}]}]}`
	var output bytes.Buffer
	require.NoError(t, normalize(strings.NewReader(fixture), &output, "api"))
	assert.Contains(t, output.String(), `"uri":"api/v1beta2/common_types.go"`)
	assert.Contains(
		t,
		output.String(),
		`"uri":"api/api/nested.go"`,
		"an api directory inside the module must not be mistaken for an already rebased path",
	)
	assert.Contains(t, output.String(), `"uri":"api/v1beta2/path%20with%20spaces.go"`)
	assert.Contains(t, output.String(), `"exact":9007199254740993`)
	assert.Contains(t, output.String(), `"informationUri":"https://example.test/gosec"`)
	assert.Contains(t, output.String(), `"startLine":1`)
}

func TestNormalizePreservesAbsoluteAndExternalURIs(t *testing.T) {
	for _, uri := range []string{"", "/checkout/api/source.go", "file:///checkout/api/source.go", "https://example.test/source.go"} {
		t.Run(uri, func(t *testing.T) {
			encoded, err := json.Marshal(map[string]any{"runs": []any{map[string]any{"artifactLocation": map[string]any{"uri": uri}}}})
			require.NoError(t, err)
			var output bytes.Buffer
			require.NoError(t, normalize(bytes.NewReader(encoded), &output, "api"))
			assert.JSONEq(t, string(encoded), output.String())
		})
	}
}

func TestNormalizeRejectsInvalidPrefixesAndReports(t *testing.T) {
	for _, prefix := range []string{"", ".", "..", "../api", "/api", "api/../other", "api\\pkg", "https://example.test"} {
		t.Run(prefix, func(t *testing.T) {
			var output bytes.Buffer
			require.ErrorIs(t, normalize(strings.NewReader(`{"runs":[]}`), &output, prefix), errInvalidPrefix)
			assert.Empty(t, output.String())
		})
	}
	for _, report := range []string{
		`{`, `null`, `{}`, `{"runs":[]} {}`, `{"runs":[{"artifactLocation":{"uri":42}}]}`,
		`{"runs":[{"artifactLocation":{"uri":"../outside.go"}}]}`,
	} {
		t.Run(report, func(t *testing.T) {
			var output bytes.Buffer
			require.Error(t, normalize(strings.NewReader(report), &output, "api"))
			assert.Empty(t, output.String())
		})
	}
}
