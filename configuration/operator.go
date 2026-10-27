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

// Package configuration provides environment defaults with explicit CLI overrides.
// The flag overlay supports Cluster API tooling without introducing config files.
package configuration

import "time"

// Operator is the top-level configuration.
//
// Probe and metrics ports follow the Numspot operator convention: /ping and
// /ready on 2112, metrics on 5555. These are deliberately NOT controller-runtime
// defaults (/healthz, /readyz, :8081, :8080); mismatching them against the Helm
// chart's probes is the single most common bootstrap bug in these repos.
type Operator struct {
	// InternalPort serves the liveness and readiness probes.
	InternalPort string `env:"INTERNAL_PORT" envDefault:"2112"`

	// MetricsPort serves Prometheus metrics.
	MetricsPort string `env:"METRICS_PORT" envDefault:"5555"`

	// LogLevel is one of debug, info, warn, error.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`

	// WatchNamespace restricts the manager's cache to one namespace. Empty means
	// all namespaces.
	WatchNamespace string `env:"WATCH_NAMESPACE" envDefault:""`

	// WatchFilterValue selects CompositeClusters carrying CAPI's watch-filter label.
	WatchFilterValue string `env:"WATCH_FILTER" envDefault:""`

	// DiagnosticsAddress overrides MetricsPort; "0" disables metrics.
	DiagnosticsAddress string `env:"DIAGNOSTICS_ADDRESS" envDefault:""`

	// HealthAddress overrides InternalPort; "0" disables probes.
	HealthAddress string `env:"HEALTH_ADDR" envDefault:""`

	// MetricsInsecure permits unauthenticated HTTP metrics for local development.
	MetricsInsecure bool `env:"METRICS_INSECURE" envDefault:"false"`

	// TLSMinVersion is the minimum diagnostics TLS protocol version.
	TLSMinVersion string `env:"TLS_MIN_VERSION" envDefault:"1.2"`

	// MetricsCertDirectory optionally supplies tls.crt and tls.key for metrics.
	MetricsCertDirectory string `env:"METRICS_CERT_DIR" envDefault:""`

	// Verbosity (0-4) overrides the log threshold; higher client-go levels can expose HTTP payloads.
	Verbosity int `env:"LOG_VERBOSITY" envDefault:"0"`

	// PrintVersion exits before reading Kubernetes configuration.
	PrintVersion bool `env:"-"`

	// SyncPeriod controls cache resync, not the two child polling cadences.
	SyncPeriod time.Duration `env:"SYNC_PERIOD" envDefault:"10h"`

	// ClientQPS and ClientBurst bound Kubernetes client request rate.
	ClientQPS   float32 `env:"KUBE_API_QPS" envDefault:"20"`
	ClientBurst int     `env:"KUBE_API_BURST" envDefault:"30"`

	// LeaderElection should be on for any replicated deployment.
	LeaderElection bool `env:"LEADER_ELECTION" envDefault:"true"`

	// LeaderElectionID is the lease name.
	LeaderElectionID string `env:"LEADER_ELECTION_ID" envDefault:"capi-composite.infrastructure.cluster.x-k8s.io"`

	// LeaderElectionNamespace overrides in-pod namespace discovery without changing watch scope.
	LeaderElectionNamespace string `env:"LEADER_ELECTION_NAMESPACE" envDefault:""`

	// Leader election timings follow controller-runtime defaults.
	LeaderLeaseDuration time.Duration `env:"LEADER_LEASE_DURATION" envDefault:"15s"`
	LeaderRenewDeadline time.Duration `env:"LEADER_RENEW_DEADLINE" envDefault:"10s"`
	LeaderRetryPeriod   time.Duration `env:"LEADER_RETRY_PERIOD" envDefault:"2s"`
}
