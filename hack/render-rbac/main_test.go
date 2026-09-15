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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_RendersStableRules(t *testing.T) {
	dir := t.TempDir()
	input, output := filepath.Join(dir, "role.yaml"), filepath.Join(dir, "rules.tpl")
	require.NoError(
		t,
		os.WriteFile(
			input,
			[]byte(
				"apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: example\nrules:\n- apiGroups: [cluster.x-k8s.io]\n  resources: [clusters]\n  verbs: [get]\n",
			),
			0o600,
		),
	)
	require.NoError(t, run([]string{input, output}))
	first, err := os.ReadFile(output)
	require.NoError(t, err)
	assert.Contains(t, string(first), `define "composite.rbacRules"`)
	assert.Contains(t, string(first), "cluster.x-k8s.io")
	require.NoError(t, run([]string{input, output}))
	second, err := os.ReadFile(output)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	require.ErrorIs(t, run(nil), errInvalidInput)
	require.NoError(t, os.WriteFile(input, []byte("kind: ClusterRole\nrules: []\n"), 0o600))
	require.ErrorIs(t, run([]string{input, output}), errInvalidInput)
}
