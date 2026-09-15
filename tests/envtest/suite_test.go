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

// Package envtest runs the integration tier against a real API server.
//
// This suite is part of ordinary go test; make test resolves pinned assets.
package envtest_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

var (
	testEnv *envtest.Environment
	cfg     *rest.Config
	k8s     client.Client
)

func TestMain(m *testing.M) {
	if os.Getenv("SKIP_ENVTEST") == "1" {
		if os.Getenv("CI") != "" {
			fmt.Fprintln(os.Stderr, "SKIP_ENVTEST is forbidden in CI")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "envtest explicitly skipped by SKIP_ENVTEST=1")
		os.Exit(0)
	}
	code, err := runSuite(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest suite: %v\n", err)
		os.Exit(1)
	}

	os.Exit(code)
}

func runSuite(m *testing.M) (int, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		apiextensionsv1.AddToScheme,
		infrav1.AddToScheme,
		// Cluster API core: the ownerRef tests call upstream's own
		// util.GetOwnerCluster, which needs a real Cluster to resolve to.
		clusterv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return 0, fmt.Errorf("building scheme: %w", err)
		}
	}

	capiCRDs, err := capiCRDPath(context.Background())
	if err != nil {
		return 0, err
	}

	// The kubeadm bootstrap provider ships its CRDs from the same module, in
	// its own tree; the reconcilers mint KubeadmConfigTemplates.
	kubeadmCRDs := filepath.Join(capiCRDs, "..", "..", "..", "bootstrap", "kubeadm", "config", "crd", "bases")

	testEnv = &envtest.Environment{
		UseExistingCluster: new(false),
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "api", "config", "crd", "bases"),
			// Upstream CAPI + kubeadm CRDs, resolved from the module we compile
			// against so the schema can never drift from the Go types in go.mod.
			// They store v1beta2, exactly what this provider writes.
			capiCRDs,
			kubeadmCRDs,
			// A stand-in provider InfraCluster, so child-object behaviour can be
			// tested without installing CAPOSC or CAPO.
			filepath.Join("testdata", "crds"),
		},
		ErrorIfCRDPathMissing: true,
		Scheme:                scheme,
	}

	cfg, err = testEnv.Start()
	if err != nil {
		return 0, fmt.Errorf("starting envtest: %w (is KUBEBUILDER_ASSETS set? run `make envtest-download`)", err)
	}

	defer func() { _ = testEnv.Stop() }()

	k8s, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return 0, fmt.Errorf("building client: %w", err)
	}

	return m.Run(), nil
}

// capiCRDPath locates the Cluster API CRDs inside the module cache. Resolving
// them from the module we build against, rather than vendoring a copy, means a
// CAPI bump cannot leave the installed schema behind the Go types.
func capiCRDPath(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}", "sigs.k8s.io/cluster-api").Output()
	if err != nil {
		return "", fmt.Errorf("locating the cluster-api module: %w", err)
	}

	return filepath.Join(strings.TrimSpace(string(out)), "config", "crd", "bases"), nil
}

// ctx returns a context bound to the test's lifetime.
func ctx(t *testing.T) context.Context {
	t.Helper()

	c, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return c
}
