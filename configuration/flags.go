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

package configuration

import (
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"

	"github.com/caarlos0/env/v11"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/validation"
)

// ErrInvalidConfiguration identifies invalid flags or environment settings.
var ErrInvalidConfiguration = errors.New("invalid operator configuration")

// Parse applies defaults, environment values and then explicitly supplied flags.
// Help is reported as pflag.ErrHelp; version requests skip runtime validation.
func Parse(args []string) (Operator, error) {
	cfg, err := env.ParseAsWithOptions[Operator](env.Options{RequiredIfNoDef: true})
	if err != nil {
		return cfg, fmt.Errorf("parsing environment: %w", err)
	}
	if cfg.DiagnosticsAddress == "" {
		cfg.DiagnosticsAddress = legacyAddress(cfg.MetricsPort)
	}
	if cfg.HealthAddress == "" {
		cfg.HealthAddress = legacyAddress(cfg.InternalPort)
	}
	flags := pflag.NewFlagSet("capi-composite", pflag.ContinueOnError)
	bindFlags(flags, &cfg)
	if err := flags.Parse(args); err != nil {
		return cfg, fmt.Errorf("parsing flags: %w", err)
	}
	if flags.NArg() != 0 {
		return cfg, fmt.Errorf("positional arguments are not supported: %w", ErrInvalidConfiguration)
	}
	if flags.Changed("v") {
		cfg.LogLevel = "info"
	}
	if cfg.PrintVersion {
		return cfg, nil
	}
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func bindFlags(flags *pflag.FlagSet, cfg *Operator) {
	flags.StringVar(&cfg.WatchNamespace, "namespace", cfg.WatchNamespace, "Namespace to watch; empty watches all namespaces")
	flags.StringVar(&cfg.WatchFilterValue, "watch-filter", cfg.WatchFilterValue, "CAPI watch-filter label value")
	flags.BoolVar(&cfg.LeaderElection, "leader-elect", cfg.LeaderElection, "Enable leader election")
	flags.StringVar(&cfg.LeaderElectionID, "leader-election-id", cfg.LeaderElectionID, "Leader election lease name")
	flags.StringVar(
		&cfg.LeaderElectionNamespace,
		"leader-election-namespace",
		cfg.LeaderElectionNamespace,
		"Lease namespace; empty uses the operator pod namespace",
	)
	flags.DurationVar(&cfg.LeaderLeaseDuration, "leader-elect-lease-duration", cfg.LeaderLeaseDuration, "Leader lease duration")
	flags.DurationVar(&cfg.LeaderRenewDeadline, "leader-elect-renew-deadline", cfg.LeaderRenewDeadline, "Leader renewal deadline")
	flags.DurationVar(&cfg.LeaderRetryPeriod, "leader-elect-retry-period", cfg.LeaderRetryPeriod, "Leader retry period")
	flags.StringVar(&cfg.DiagnosticsAddress, "diagnostics-address", cfg.DiagnosticsAddress, "Metrics bind address; 0 disables metrics")
	flags.BoolVar(&cfg.MetricsInsecure, "insecure-diagnostics", cfg.MetricsInsecure, "Serve unauthenticated HTTP metrics (development only)")
	flags.StringVar(&cfg.HealthAddress, "health-addr", cfg.HealthAddress, "Health probe bind address; 0 disables probes")
	flags.StringVar(&cfg.TLSMinVersion, "tls-min-version", cfg.TLSMinVersion, "Minimum metrics TLS version: 1.2 or 1.3")
	flags.StringVar(&cfg.MetricsCertDirectory, "metrics-cert-dir", cfg.MetricsCertDirectory, "Directory containing metrics tls.crt and tls.key")
	flags.DurationVar(&cfg.SyncPeriod, "sync-period", cfg.SyncPeriod, "Cache resync period; does not change child polling intervals")
	flags.Float32Var(&cfg.ClientQPS, "kube-api-qps", cfg.ClientQPS, "Kubernetes client requests per second")
	flags.IntVar(&cfg.ClientBurst, "kube-api-burst", cfg.ClientBurst, "Kubernetes client request burst")
	flags.IntVar(&cfg.Verbosity, "v", cfg.Verbosity, "Diagnostic log verbosity (0-4; higher levels can expose HTTP payloads)")
	flags.BoolVar(&cfg.PrintVersion, "version", false, "Print version and exit")
}

func legacyAddress(port string) string {
	if port == "0" {
		return "0"
	}
	return net.JoinHostPort("", port)
}

// MinimumTLSVersion returns the validated diagnostics protocol floor.
func (o *Operator) MinimumTLSVersion() uint16 {
	if o.TLSMinVersion == "1.3" {
		return tls.VersionTLS13
	}
	return tls.VersionTLS12
}

func (o *Operator) validate() error {
	for _, address := range []string{o.DiagnosticsAddress, o.HealthAddress} {
		if err := validateAddress(address); err != nil {
			return err
		}
	}
	if err := o.validateScope(); err != nil {
		return err
	}
	if err := o.validateTuning(); err != nil {
		return err
	}
	return nil
}

func (o *Operator) validateScope() error {
	if o.WatchNamespace != "" && len(validation.IsDNS1123Label(o.WatchNamespace)) != 0 {
		return fmt.Errorf("namespace must be a DNS label: %w", ErrInvalidConfiguration)
	}
	if o.LeaderElectionNamespace != "" && len(validation.IsDNS1123Label(o.LeaderElectionNamespace)) != 0 {
		return fmt.Errorf("leader-election-namespace must be a DNS label: %w", ErrInvalidConfiguration)
	}
	if len(validation.IsValidLabelValue(o.WatchFilterValue)) != 0 {
		return fmt.Errorf("watch-filter must be a Kubernetes label value: %w", ErrInvalidConfiguration)
	}
	if len(validation.IsDNS1123Subdomain(o.LeaderElectionID)) != 0 {
		return fmt.Errorf("leader-election-id must be a DNS subdomain: %w", ErrInvalidConfiguration)
	}
	return nil
}

func (o *Operator) validateTuning() error {
	if o.LeaderLeaseDuration <= o.LeaderRenewDeadline || o.LeaderRenewDeadline <= o.LeaderRetryPeriod || o.LeaderRetryPeriod <= 0 {
		return fmt.Errorf("leader timings must satisfy lease > renew > retry > 0: %w", ErrInvalidConfiguration)
	}
	if !(o.ClientQPS > 0) || math.IsInf(float64(o.ClientQPS), 0) || o.ClientBurst <= 0 {
		return fmt.Errorf("client QPS must be finite and positive and burst must be positive: %w", ErrInvalidConfiguration)
	}
	if o.SyncPeriod <= 0 || o.Verbosity < 0 || o.Verbosity > 4 {
		return fmt.Errorf("sync-period must be positive and verbosity must be between 0 and 4: %w", ErrInvalidConfiguration)
	}
	return o.validateLoggingAndTLS()
}

func (o *Operator) validateLoggingAndTLS() error {
	if o.TLSMinVersion != "1.2" && o.TLSMinVersion != "1.3" {
		return fmt.Errorf("TLS minimum must be 1.2 or 1.3: %w", ErrInvalidConfiguration)
	}
	switch strings.ToLower(o.LogLevel) {
	case "debug", "info", "warn", "warning", "error":
		return nil
	default:
		return fmt.Errorf("unknown log level: %w", ErrInvalidConfiguration)
	}
}

func validateAddress(address string) error {
	if address == "0" {
		return nil
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parsing bind address: %w", err)
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("parsing bind port: %w", err)
	}
	if value < 0 || value > 65535 {
		return fmt.Errorf("bind port is out of range: %w", ErrInvalidConfiguration)
	}
	return nil
}
