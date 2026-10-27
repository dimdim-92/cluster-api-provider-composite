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

// Command crd-labels stamps the Cluster API contract labels onto the CRDs
// emitted by controller-gen.
//
// Why this exists at all:
//
// In Cluster API v1beta2, Cluster.spec.infrastructureRef is a
// ContractVersionedObjectReference carrying apiGroup, kind and name -- but no
// version. CAPI resolves the version at runtime by fetching the CRD and reading
// the label cluster.x-k8s.io/<contract>. Without that label CAPI cannot resolve
// the reference at all and fails with "cannot find any versions matching
// contract versions".
//
// That failure is invisible until the first Cluster referencing the CRD is
// created: controller-gen succeeds, kubectl apply succeeds, and the object
// simply never reconciles. The earlier in-house prototype shipped CRDs without
// these labels for exactly this reason.
//
// controller-gen cannot emit labels, and the usual kubebuilder answer -- a
// kustomize commonLabels overlay -- would introduce a second source of truth for
// what a shipped CRD looks like, since the rest of this repo (matching the
// house operator layout) treats api/config/crd/bases as the shippable output and
// copies it straight into the Helm chart. So the labels are stamped in place
// here, idempotently, as part of `make gen`, and re-checked by
// `make verify-crd-labels` in CI.
//
// See docs/design.md for contract labels and native-version resolution.
package main

import (
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"sigs.k8s.io/yaml"
)

var (
	// errNoCRDs means controller-gen produced nothing, so stamping would
	// silently "succeed" on an empty set and CI would pass with no CRDs.
	errNoCRDs = errors.New("no CRDs found: did controller-gen run?")

	// errNotACRD means a stray YAML file landed in the CRD output directory.
	errNotACRD = errors.New("expected a CustomResourceDefinition")

	// errNoMetadata means a CRD document has no metadata object.
	errNoMetadata      = errors.New("metadata is missing or not an object")
	errUnknownResource = errors.New("unsupported Composite CRD identity")
	errIncorrectLabels = errors.New("CRD labels do not match the resource's contract")
)

// contractLabels apply to the InfraCluster and template APIs. Configuration
// profiles receive the provider label only, never an InfraCluster contract label.
var contractLabels = map[string]string{
	// Declares that our v1beta2 API version implements the v1beta2 CAPI
	// contract. The key is the contract, the value is the list of API versions
	// implementing it.
	"cluster.x-k8s.io/v1beta2": "v1beta2",
	// Identifies the owning provider to clusterctl and to CAPI tooling.
	"cluster.x-k8s.io/provider": "infrastructure-composite",
}

var contractLabelKey = regexp.MustCompile(`^cluster\.x-k8s\.io/v\d+((alpha|beta)\d+)?$`)

func main() {
	check := flag.Bool("check", false, "verify labels without changing files")
	flag.Parse()
	if len(flag.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "usage: crd-labels [-check] <dir>...")
		os.Exit(2)
	}

	var changed int

	for _, dir := range flag.Args() {
		paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
		if err != nil {
			fatal(fmt.Errorf("glob %s: %w", dir, err))
		}

		if len(paths) == 0 {
			fatal(fmt.Errorf("%s: %w", dir, errNoCRDs))
		}

		sort.Strings(paths)

		for _, p := range paths {
			ok, err := reconcileLabels(p, !*check)
			if err != nil {
				fatal(err)
			}

			if ok {
				changed++

				fmt.Printf("crd-labels: stamped %s\n", filepath.Base(p))
			}
		}
	}

	if *check {
		fmt.Println("crd-labels: resource-specific labels verified")
		return
	}
	fmt.Printf("crd-labels: %d file(s) updated\n", changed)
}

// reconcileLabels checks resource-specific labels, optionally repairing them.
func reconcileLabels(path string, write bool) (bool, error) {
	doc, err := readCRD(path)
	if err != nil {
		return false, err
	}

	meta, ok := doc["metadata"].(map[string]any)
	if !ok {
		return false, fmt.Errorf("%s: %w", path, errNoMetadata)
	}

	infraContract, err := implementsInfraContract(doc)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	labels, dirty := applyLabels(meta, infraContract)
	if !dirty {
		return false, nil
	}
	if !write {
		return false, fmt.Errorf("%s: %w", path, errIncorrectLabels)
	}

	meta["labels"] = labels

	if err := writeCRD(path, doc); err != nil {
		return false, err
	}

	return true, nil
}

func implementsInfraContract(doc map[string]any) (bool, error) {
	spec, ok := doc["spec"].(map[string]any)
	if !ok || spec["group"] != "infrastructure.cluster.x-k8s.io" {
		return false, errUnknownResource
	}
	names, ok := spec["names"].(map[string]any)
	if !ok {
		return false, errUnknownResource
	}
	switch names["kind"] {
	case "CompositeCluster", "CompositeClusterTemplate":
		return true, nil
	case "CompositeProvider", "CompositeProviderTemplate":
		return false, nil
	default:
		return false, errUnknownResource
	}
}

func readCRD(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if kind, _ := doc["kind"].(string); kind != "CustomResourceDefinition" {
		return nil, fmt.Errorf("%s: %w, got %q", path, errNotACRD, kind)
	}

	return doc, nil
}

// applyLabels returns the label set with the contract labels present, and
// whether anything changed.
func applyLabels(meta map[string]any, infraContract bool) (map[string]any, bool) {
	labels, ok := meta["labels"].(map[string]any)
	if !ok {
		labels = map[string]any{}
	}

	dirty := false
	for key := range labels {
		if contractLabelKey.MatchString(key) && (!infraContract || key != "cluster.x-k8s.io/v1beta2") {
			delete(labels, key)
			dirty = true
		}
	}

	wanted := contractLabels
	if !infraContract {
		wanted = maps.Clone(contractLabels)
		delete(wanted, "cluster.x-k8s.io/v1beta2")
	}
	for k, v := range wanted {
		if cur, found := labels[k]; !found || cur != v {
			labels[k] = v
			dirty = true
		}
	}

	return labels, dirty
}

func writeCRD(path string, doc map[string]any) error {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	// controller-gen writes a leading document separator; keep the files
	// byte-comparable with its output style.
	out = append([]byte("---\n"), out...)

	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "crd-labels: %v\n", err)
	os.Exit(1)
}
