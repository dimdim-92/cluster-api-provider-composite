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

// Package capi holds the Cluster API contract plumbing: resolving a child's
// contract version, reading its status through the right field paths, applying
// children with the correct ownership, and dry-run validating them against the
// target CRD.
package capi

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ContractVersion identifies which revision of the CAPI provider contract an
// object implements. It decides where readiness lives, and nothing else in this
// package should branch on provider identity to answer that question.
type ContractVersion string

const (
	// ContractV1Beta1 puts provisioned-ness at status.ready.
	ContractV1Beta1 ContractVersion = "v1beta1"

	// ContractV1Beta2 puts it at status.initialization.provisioned.
	ContractV1Beta2 ContractVersion = "v1beta2"
)

// contractLabelPrefix is the CRD label prefix CAPI uses to advertise which
// contract a CRD's API versions implement, e.g.
//
//	cluster.x-k8s.io/v1beta2: v1beta2_v1beta1
//
// The KEY names the contract; the VALUE lists the CRD's API versions that
// implement it, underscore-joined. Both halves are read: the contract decides
// where readiness lives, the version decides what apiVersion the child is
// written and read at. Pairing the contract with the storage version instead
// would silently read readiness at the wrong path on a CRD whose storage
// version implements an older contract than its newest label.
const contractLabelPrefix = "cluster.x-k8s.io/"

// ChildContract is the resolved contract information for one child GroupKind.
type ChildContract struct {
	// GroupKind is the child kind this applies to.
	GroupKind schema.GroupKind

	// Contract is the newest contract the CRD advertises.
	Contract ContractVersion

	// APIVersion is the served API version selected by the contract labels, e.g.
	// "infrastructure.cluster.x-k8s.io/v1beta1". Recorded on generated children
	// so an object states which version it was written against.
	APIVersion string
}

// ContractCache resolves and caches, per child GroupKind, the CAPI contract
// version and served API version taken from the CRD's labels.
//
// This exists because we sit on both sides of a contract boundary at once: we
// serve v1beta2, while both providers we currently generate children for
// (CAPOSC and CAPO) are still v1beta1. Hardcoding either field path would work
// today and silently break on the next provider bump -- readiness would simply
// read as false forever, with no error anywhere.
//
// Entries are reused only while the freshly read CRD resourceVersion matches.
// Child-provider upgrades do not require restarting the composite manager.
type ContractCache struct {
	client client.Reader

	mu    sync.RWMutex
	cache map[schema.GroupKind]contractEntry
}

type contractEntry struct {
	resourceVersion string
	contract        ChildContract
}

// NewContractCache returns a cache reading CRDs through c. Callers pass the
// manager's API reader rather than its cached client: a CRD Get through the
// cache would start a cluster-wide informer over every CRD for the handful
// of lookups this cache ever makes, and the Service Cluster's control plane
// is memory-starved.
func NewContractCache(c client.Reader) *ContractCache {
	return &ContractCache{
		client: c,
		cache:  make(map[schema.GroupKind]contractEntry),
	}
}

// Get resolves the contract against the current CRD before reusing cached parsing.
//
// A NotFound on the CRD is returned as ErrCRDNotInstalled rather than a generic
// error: the caller disables that provider instead of failing the cluster, so
// one image stays deployable to a Service Cluster where only some providers are
// installed.
func (r *ContractCache) Get(ctx context.Context, gk schema.GroupKind) (ChildContract, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: crdName(gk)}, crd); err != nil {
		if apierrors.IsNotFound(err) {
			return ChildContract{}, fmt.Errorf("%s: %w", crdName(gk), ErrCRDNotInstalled)
		}
		return ChildContract{}, fmt.Errorf("ContractCache.Get: reading %s: %w", crdName(gk), err)
	}
	r.mu.RLock()
	hit, ok := r.cache[gk]
	r.mu.RUnlock()

	if ok && hit.resourceVersion == crd.ResourceVersion {
		return hit.contract, nil
	}

	resolved, err := resolveContract(gk, crd)
	if err != nil {
		return ChildContract{}, err
	}

	r.mu.Lock()
	r.cache[gk] = contractEntry{resourceVersion: crd.ResourceVersion, contract: resolved}
	r.mu.Unlock()

	return resolved, nil
}

var (
	// ErrCRDNotInstalled means the child's CRD is absent from this cluster, so
	// the provider that generates it cannot be used here. Callers disable that
	// provider rather than failing the cluster.
	ErrCRDNotInstalled = errors.New("CRD is not installed")

	// ErrNoContractLabel means a CRD carries no cluster.x-k8s.io/<contract>
	// label. CAPI cannot resolve references to such a CRD at all, so this is a
	// hard error rather than a defaulted guess.
	ErrNoContractLabel = errors.New("CRD has no cluster.x-k8s.io/<contract> label; CAPI cannot resolve references to it")

	// ErrInvalidContract means the advertised contract has no usable namespaced API.
	ErrInvalidContract = errors.New("CRD has no usable namespaced contract version")
)

func resolveContract(gk schema.GroupKind, crd *apiextensionsv1.CustomResourceDefinition) (ChildContract, error) {
	if crd.Spec.Scope != apiextensionsv1.NamespaceScoped || crd.DeletionTimestamp != nil {
		return ChildContract{}, fmt.Errorf("%s: %w", crd.Name, ErrInvalidContract)
	}
	if _, _, ok := contractFromLabels(crd.Labels); !ok {
		return ChildContract{}, fmt.Errorf("%s: %w", crd.Name, ErrNoContractLabel)
	}
	served := map[string]bool{}
	for _, v := range crd.Spec.Versions {
		served[v.Name] = v.Served
	}
	labels := maps.Clone(crd.Labels)
	for _, contract := range []ContractVersion{ContractV1Beta2, ContractV1Beta1} {
		key := contractLabelPrefix + string(contract)
		versions := slices.DeleteFunc(strings.Split(labels[key], "_"), func(v string) bool { return !served[v] })
		labels[key] = strings.Join(versions, "_")
	}
	contract, apiVersion, ok := contractFromLabels(labels)
	if !ok {
		return ChildContract{}, fmt.Errorf("%s: %w", crd.Name, ErrInvalidContract)
	}

	return ChildContract{GroupKind: gk, Contract: contract, APIVersion: crd.Spec.Group + "/" + apiVersion}, nil
}

// crdName renders the CRD name for a GroupKind. controller-gen lowercases the
// kind and appends "s", which matches how every CAPI provider names its CRDs.
func crdName(gk schema.GroupKind) string {
	return fmt.Sprintf("%ss.%s", strings.ToLower(gk.Kind), gk.Group)
}

// contractFromLabels picks the newest contract advertised by the CRD and the
// newest API version listed under that label — the same choice CAPI's
// GetLatestContractAndAPIVersionFromContract makes. A CRD may advertise
// several (a provider mid-migration serves both), and we want the newest we
// understand; a label with an empty value is treated as absent, as CAPI does.
func contractFromLabels(labels map[string]string) (ContractVersion, string, bool) {
	// Ordered newest first.
	for _, want := range []ContractVersion{ContractV1Beta2, ContractV1Beta1} {
		value, ok := labels[contractLabelPrefix+string(want)]
		if !ok || value == "" {
			continue
		}

		versions := strings.Split(value, "_")
		sort.Slice(versions, func(i, j int) bool {
			return version.CompareKubeAwareVersionStrings(versions[i], versions[j]) < 0
		})

		return want, versions[len(versions)-1], true
	}

	return "", "", false
}
