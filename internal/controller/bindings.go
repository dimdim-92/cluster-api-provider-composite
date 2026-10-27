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

package controller

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
)

var errInvalidComposition = errors.New("provider composition is invalid")

const (
	compositeClusterKind         = "CompositeCluster"
	compositeClusterTemplateKind = "CompositeClusterTemplate"
	compositeProviderKind        = "CompositeProvider"
	maxProviderBindings          = 8
	maxProviderBindingsBytes     = 16 * 1024
)

type providerBinding struct {
	APIGroup               string         `json:"apiGroup"`
	Kind                   string         `json:"kind"`
	ChildName              string         `json:"childName,omitempty"`
	BootstrapName          string         `json:"bootstrapName,omitempty"`
	ProfileName            string         `json:"profileName,omitempty"`
	ProfileUID             types.UID      `json:"profileUID,omitempty"`
	InfrastructureRevision sourceRevision `json:"infrastructureRevision,omitempty,omitzero"`
	BootstrapRevision      sourceRevision `json:"bootstrapRevision,omitempty,omitzero"`
	Provider               string         `json:"provider,omitempty"`
	Account                string         `json:"account,omitempty"`
	Region                 string         `json:"region,omitempty"`
}

// sourceRevision detects normal template spec edits and replacement without hashing content.
type sourceRevision struct {
	UID        types.UID `json:"uid"`
	Generation int64     `json:"generation"`
}

func (s sourceRevision) valid() bool {
	return validSourceUID(s.UID) && s.Generation > 0
}

func validSourceUID(uid types.UID) bool {
	return uid != "" && len(uid) <= 128 && utf8.ValidString(string(uid))
}

func revisionOf(obj client.Object) sourceRevision {
	return sourceRevision{UID: obj.GetUID(), Generation: obj.GetGeneration()}
}

func (b *providerBinding) groupKind() schema.GroupKind {
	return schema.GroupKind{Group: b.APIGroup, Kind: b.Kind}
}

func validBindingRecord(b *providerBinding) bool {
	return len(validation.IsDNS1123Subdomain(b.ChildName)) == 0 &&
		len(validation.IsDNS1123Subdomain(b.BootstrapName)) == 0 && validProfileIdentity(b) &&
		validSourceUID(b.ProfileUID) && b.InfrastructureRevision.valid() && b.BootstrapRevision.valid()
}

func (b *providerBinding) profileStatus() infrav1.ProviderProfileStatus {
	if b.ProfileName == "" {
		return infrav1.ProviderProfileStatus{}
	}
	return infrav1.ProviderProfileStatus{
		Name:     b.ProfileName,
		Provider: infrav1.ProviderName(b.Provider), Account: b.Account, Region: b.Region,
	}
}

func validProfileIdentity(b *providerBinding) bool {
	return len(validation.IsDNS1123Subdomain(b.ProfileName)) == 0 && len(validation.IsDNS1123Label(b.Provider)) == 0 &&
		boundedIdentity(b.Account) && boundedIdentity(b.Region)
}

func boundedIdentity(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 63
}

type nativeChildIdentity struct {
	groupKind schema.GroupKind
	name      string
}

func validateCanonicalBindings(bindings map[string]providerBinding, canonical map[schema.GroupKind]bool) error {
	for _, name := range slices.Sorted(maps.Keys(bindings)) {
		binding := bindings[name]
		if !canonical[binding.groupKind()] {
			return fmt.Errorf("provider %q requires a canonical child for %s: %w", name, binding.groupKind(), errInvalidComposition)
		}
	}
	return nil
}

func validProviderTarget(binding *providerBinding) bool {
	return binding.APIGroup != "" && binding.Kind != "" && !isCompositeKind(binding.APIGroup, binding.Kind)
}

func isCompositeKind(group, kind string) bool {
	return group == infrav1.GroupVersion.Group &&
		(kind == compositeClusterKind || kind == compositeClusterTemplateKind || kind == compositeProviderKind || kind == "CompositeProviderTemplate")
}

func (r *CompositeClusterReconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func compositeOwnership(cc *infrav1.CompositeCluster) capi.Ownership {
	own := capi.Ownership{Controller: cc}
	for _, ref := range cc.OwnerReferences {
		if ref.Kind == clusterv1.ClusterKind && schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind).Group == clusterv1.GroupVersion.Group {
			own.Cluster = &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: cc.Namespace, UID: ref.UID}}
			break
		}
	}
	return own
}
