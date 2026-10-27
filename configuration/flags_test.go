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

package configuration_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/numspot/cluster-api-provider-composite/configuration"
)

func TestParse_FlagsOverrideEnvironment(t *testing.T) {
	t.Setenv("WATCH_NAMESPACE", "from-env")
	t.Setenv("LEADER_ELECTION", "true")
	t.Setenv("METRICS_PORT", "5556")
	t.Setenv("LOG_LEVEL", "debug")
	cfg, err := configuration.Parse([]string{
		"--namespace=", "--leader-elect=false", "--diagnostics-address=0", "--v=0",
	})
	require.NoError(t, err)
	assert.Empty(t, cfg.WatchNamespace)
	assert.False(t, cfg.LeaderElection)
	assert.Equal(t, "0", cfg.DiagnosticsAddress)
	assert.Equal(t, "info", cfg.LogLevel)
}

func TestParse_PreservesEnvironmentDefaults(t *testing.T) {
	t.Setenv("WATCH_NAMESPACE", "from-env")
	t.Setenv("INTERNAL_PORT", "2113")
	t.Setenv("METRICS_PORT", "0")
	t.Setenv("HEALTH_ADDR", "")
	t.Setenv("DIAGNOSTICS_ADDRESS", "")
	cfg, err := configuration.Parse(nil)
	require.NoError(t, err)
	assert.Equal(t, "from-env", cfg.WatchNamespace)
	assert.Equal(t, ":2113", cfg.HealthAddress)
	assert.Equal(t, "0", cfg.DiagnosticsAddress)
	assert.False(t, cfg.MetricsInsecure)
	assert.Equal(t, 15*time.Second, cfg.LeaderLeaseDuration)
}

func TestParse_RejectsInvalidConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"--unknown"},
		{"--provider-kinds=fake=infrastructure.cluster.x-k8s.io/FakeCluster"},
		{"positional"},
		{"--namespace=INVALID_NAMESPACE"},
		{"--diagnostics-address=not-an-address"},
		{"--tls-min-version=1.0"},
		{"--leader-elect-lease-duration=1s"},
		{"--kube-api-burst=0"},
		{"--v=-1"},
		{"--v=5"},
	} {
		t.Run(args[0], func(t *testing.T) {
			_, err := configuration.Parse(args)
			require.Error(t, err)
		})
	}
}

func TestParse_VersionDoesNotNeedClusterConfiguration(t *testing.T) {
	t.Setenv("KUBECONFIG", "/does-not-exist")
	cfg, err := configuration.Parse([]string{"--version"})
	require.NoError(t, err)
	assert.True(t, cfg.PrintVersion)
}
