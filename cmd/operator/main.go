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

// Command operator runs the capi-composite controller manager.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgofeatures "k8s.io/client-go/features"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/configuration"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
	"github.com/numspot/cluster-api-provider-composite/internal/controller"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("Operator exited with an error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	// Set the client policy before constructing any Kubernetes clients or caches.
	clientgofeatures.ReplaceFeatureGates(operatorFeatureGates{Gates: clientgofeatures.FeatureGates()})

	cfg, err := configuration.Parse(os.Args[1:])
	if errors.Is(err, pflag.ErrHelp) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("configuration.Parse: %w", err)
	}
	if cfg.PrintVersion {
		fmt.Println(Version)
		return nil
	}

	initLogger(&cfg)

	slog.Info("Started capi-composite",
		slog.String("version", Version),
		slog.String("watchNamespace", cfg.WatchNamespace))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mgr, err := newManager(&cfg)
	if err != nil {
		return err
	}

	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("manager.Start: %w", err)
	}

	return nil
}

// initLogger wires log/slog as the single logging backend and bridges
// controller-runtime's logr onto it, so controller output and our own output
// share one handler, one format and one destination.
func initLogger(cfg *configuration.Operator) {
	level := parseLevel(cfg.LogLevel)
	if cfg.Verbosity > 0 {
		level = slog.Level(-cfg.Verbosity)
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})

	logger := slog.New(handler).With(
		slog.String("service", "capi-composite"),
		slog.String("version", Version))
	slog.SetDefault(logger)

	ctrl.SetLogger(logr.FromSlogHandler(logger.Handler()))
	klog.SetSlogLogger(logger)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// operatorFeatureGates always initializes informer caches with LIST then WATCH.
// Other client-go features retain their defaults and environment configuration.
type operatorFeatureGates struct {
	clientgofeatures.Gates
}

func (g operatorFeatureGates) Enabled(feature clientgofeatures.Feature) bool {
	return feature != clientgofeatures.WatchListClient && g.Gates.Enabled(feature)
}

// newScheme registers everything the manager reads or writes with typed clients.
//
// Provider children (OscCluster, OpenStackCluster, their MachineTemplates) are
// deliberately absent: they are handled as unstructured so this binary does not
// depend on any provider's Go module. See internal/capi.ChildStatus.
func newScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()

	for name, add := range map[string]func(*runtime.Scheme) error{
		"client-go":      clientgoscheme.AddToScheme,
		"capi-composite": infrav1.AddToScheme,
		// CAPI core: Cluster (owner resolution, pause, endpoint fallback).
		"cluster-api": clusterv1.AddToScheme,
		// KubeadmConfigTemplate, minted per provider.
		"kubeadm-bootstrap": bootstrapv1.AddToScheme,
		// CRDs, read to resolve each child's contract version.
		"apiextensions": apiextensionsv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			return nil, fmt.Errorf("newScheme: adding %s types: %w", name, err)
		}
	}

	return s, nil
}

func managerOptions(cfg *configuration.Operator, scheme *runtime.Scheme) ctrl.Options {
	opts := ctrl.Options{
		Scheme: scheme,
		// Numspot operator convention: probes on 2112 at /ping and /ready,
		// metrics on 5555. These differ from the controller-runtime defaults and
		// must match the Helm chart's probe paths.
		Metrics: metricsserver.Options{
			BindAddress:   cfg.DiagnosticsAddress,
			SecureServing: !cfg.MetricsInsecure,
			CertDir:       cfg.MetricsCertDirectory,
			TLSOpts: []func(*tls.Config){func(c *tls.Config) {
				c.MinVersion = cfg.MinimumTLSVersion()
				c.NextProtos = []string{"http/1.1"}
			}},
		},
		HealthProbeBindAddress:  cfg.HealthAddress,
		LivenessEndpointName:    "/ping",
		ReadinessEndpointName:   "/ready",
		LeaderElection:          cfg.LeaderElection,
		LeaderElectionID:        cfg.LeaderElectionID,
		LeaderElectionNamespace: cfg.LeaderElectionNamespace,
		LeaseDuration:           &cfg.LeaderLeaseDuration,
		RenewDeadline:           &cfg.LeaderRenewDeadline,
		RetryPeriod:             &cfg.LeaderRetryPeriod,
		Cache:                   cache.Options{SyncPeriod: &cfg.SyncPeriod},
	}
	if !cfg.MetricsInsecure {
		opts.Metrics.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	if cfg.WatchNamespace != "" {
		opts.Cache.DefaultNamespaces = map[string]cache.Config{cfg.WatchNamespace: {}}
	}
	return opts
}

// Native admission warnings can contain the same sensitive values as rejected
// payload errors. Keep their text out of the shared logging pipeline.
type safeAPIWarnings struct{}

func (safeAPIWarnings) HandleWarningHeaderWithContext(ctx context.Context, code int, _, _ string) {
	ctrl.LoggerFrom(ctx).Info("API server warning received; details withheld to protect native payloads", "code", code)
}

func newManager(cfg *configuration.Operator) (manager.Manager, error) {
	scheme, err := newScheme()
	if err != nil {
		return nil, err
	}
	config, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("ctrl.GetConfig: %w", err)
	}
	config.QPS, config.Burst = cfg.ClientQPS, cfg.ClientBurst
	config.WarningHandlerWithContext = safeAPIWarnings{}
	mgr, err := ctrl.NewManager(config, managerOptions(cfg, scheme))
	if err != nil {
		return nil, fmt.Errorf("ctrl.NewManager: %w", err)
	}

	if err := addChecks(mgr); err != nil {
		return nil, err
	}

	if err := addReconcilers(mgr, cfg); err != nil {
		return nil, err
	}

	return mgr, nil
}

// addReconcilers wires the controllers over the native contract cache.
// Provider templates declare the native GroupKinds for attachments.
func addReconcilers(mgr manager.Manager, cfg *configuration.Operator) error {
	contracts := capi.NewContractCache(mgr.GetAPIReader())

	clusterReconciler := &controller.CompositeClusterReconciler{
		Client:           mgr.GetClient(),
		Contracts:        contracts,
		WatchFilterValue: cfg.WatchFilterValue,
	}
	if err := clusterReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("addReconcilers: CompositeCluster: %w", err)
	}
	profileReconciler := &controller.CompositeProviderTemplateReconciler{Client: mgr.GetClient()}
	if err := profileReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("addReconcilers: CompositeProvider: %w", err)
	}

	return nil
}

func addChecks(mgr manager.Manager) error {
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return fmt.Errorf("manager.AddHealthzCheck: %w", err)
	}

	if err := mgr.AddReadyzCheck("ready", healthz.Ping); err != nil {
		return fmt.Errorf("manager.AddReadyzCheck: %w", err)
	}

	return nil
}
