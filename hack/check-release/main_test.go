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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRun_ValidatesReleaseInputs(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(
		t,
		os.WriteFile(
			"metadata.yaml",
			[]byte("apiVersion: clusterctl.cluster.x-k8s.io/v1alpha3\nkind: Metadata\nreleaseSeries:\n- major: 0\n  minor: 1\n  contract: v1beta2\n"),
			0o600,
		),
	)
	require.NoError(t, run("v0.1.0-alpha.1", "example.org/composite:v0.1.0-alpha.1"))
	for _, version := range []string{"0.1.0", "v0.1.0-alpha..1", "v0.2.0", "v01.1.0"} {
		require.Error(t, run(version, "example.org/composite:dev"), version)
	}
	for _, image := range []string{"", "example.org/composite", "example.org/INVALID:dev", "example.org/composite@@invalid"} {
		require.Error(t, run("v0.1.0-alpha.1", image), image)
	}
}
