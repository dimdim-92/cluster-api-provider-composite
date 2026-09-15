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
	"crypto/tls"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	clientgofeatures "k8s.io/client-go/features"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/numspot/cluster-api-provider-composite/configuration"
)

func TestRunDisablesWatchList(t *testing.T) {
	const helperEnv = "COMPOSITE_TEST_CLIENT_FEATURES"
	if os.Getenv(helperEnv) == "1" {
		// Exercise real startup without loading a kubeconfig or contacting an API.
		os.Args = []string{"operator", "--version"}
		require.NoError(t, run())
		assert.False(t, clientgofeatures.FeatureGates().Enabled(clientgofeatures.WatchListClient))
		assert.True(
			t,
			clientgofeatures.FeatureGates().Enabled(clientgofeatures.ClientsPreferCBOR),
			"unrelated feature gates retain their environment configuration",
		)
		return
	}

	executable, err := os.Executable()
	require.NoError(t, err)
	for _, value := range []string{"unset", "true", "false"} {
		t.Run(value, func(t *testing.T) {
			// client-go reads environment feature gates once; isolate each case.
			cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestRunDisablesWatchList$")
			for _, env := range os.Environ() {
				if !strings.HasPrefix(env, "KUBE_FEATURE_") && !strings.HasPrefix(env, helperEnv+"=") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env, helperEnv+"=1", "KUBE_FEATURE_ClientsPreferCBOR=true")
			if value != "unset" {
				cmd.Env = append(cmd.Env, "KUBE_FEATURE_WatchListClient="+value)
			}
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "checking startup feature gates: %s", output)
		})
	}
}

func TestManagerOptions_SecureMetricsAndNamespace(t *testing.T) {
	cfg, err := configuration.Parse([]string{"--namespace=watched", "--tls-min-version=1.3"})
	require.NoError(t, err)
	opts := managerOptions(&cfg, runtime.NewScheme())
	assert.True(t, opts.Metrics.SecureServing)
	assert.NotNil(t, opts.Metrics.FilterProvider)
	assert.Contains(t, opts.Cache.DefaultNamespaces, "watched")
	assert.Equal(t, cfg.SyncPeriod, *opts.Cache.SyncPeriod)
	tlsConfig := &tls.Config{}
	for _, configure := range opts.Metrics.TLSOpts {
		configure(tlsConfig)
	}
	assert.Equal(t, uint16(tls.VersionTLS13), tlsConfig.MinVersion)
	assert.Equal(t, []string{"http/1.1"}, tlsConfig.NextProtos)
	assert.Equal(t, "/ping", opts.LivenessEndpointName)
	assert.Equal(t, "/ready", opts.ReadinessEndpointName)
}

func TestManagerOptions_InsecureMetricsIsExplicit(t *testing.T) {
	cfg, err := configuration.Parse([]string{"--insecure-diagnostics=true", "--namespace="})
	require.NoError(t, err)
	opts := managerOptions(&cfg, runtime.NewScheme())
	assert.False(t, opts.Metrics.SecureServing)
	assert.Nil(t, opts.Metrics.FilterProvider)
	assert.Empty(t, opts.Cache.DefaultNamespaces)
}

func TestManagerOptions_LeaseNamespaceIsIndependentOfWatchScope(t *testing.T) {
	t.Setenv("LEADER_ELECTION_NAMESPACE", "from-env")
	cfg, err := configuration.Parse([]string{"--namespace=workloads", "--leader-election-namespace=management"})
	require.NoError(t, err)
	opts := managerOptions(&cfg, runtime.NewScheme())
	assert.Equal(t, "management", opts.LeaderElectionNamespace)
	assert.Contains(t, opts.Cache.DefaultNamespaces, "workloads")
	cfg, err = configuration.Parse([]string{"--leader-election-namespace="})
	require.NoError(t, err)
	assert.Empty(t, managerOptions(&cfg, runtime.NewScheme()).LeaderElectionNamespace, "empty preserves in-pod namespace discovery")
	_, err = configuration.Parse([]string{"--leader-election-namespace=INVALID_NAMESPACE"})
	require.Error(t, err)
}

func TestAPIWarningsDoNotDiscloseNativePayloads(t *testing.T) {
	var output bytes.Buffer
	ctx := ctrl.LoggerInto(t.Context(), logr.FromSlogHandler(slog.NewJSONHandler(&output, nil)))
	safeAPIWarnings{}.HandleWarningHeaderWithContext(ctx, 299, "secret-agent", "secret-native-payload")
	assert.NotContains(t, output.String(), "secret-agent")
	assert.NotContains(t, output.String(), "secret-native-payload")
	assert.Contains(t, output.String(), "API server warning")
}
