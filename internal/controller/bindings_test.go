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
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func attachmentBindingsFixture() (*infrav1.CompositeCluster, attachmentBindings) {
	cc := &infrav1.CompositeCluster{ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: "outside", UID: "cluster-uid"}}
	binding := providerBinding{
		APIGroup:      "infra.test",
		Kind:          "FakeCluster",
		ChildName:     cc.Name,
		BootstrapName: cc.Name + "-regional",
		ProfileName:   "profile-a",
		ProfileUID:    "profile-uid",
		Provider:      "fake",
		Account:       "account-a",
		Region:        "region-a",
		InfrastructureRevision: sourceRevision{
			UID:        "infra-uid",
			Generation: 1,
		},
		BootstrapRevision: sourceRevision{UID: "bootstrap-uid", Generation: 1},
	}
	return cc, attachmentBindings{
		Version:   1,
		Providers: map[string]*attachmentBinding{"regional": {UID: "attachment-uid", ClusterUID: cc.UID, Binding: binding}},
	}
}

func TestAttachmentBindingsValidation(t *testing.T) {
	for name, change := range map[string]func(*attachmentBinding){
		"missing-child":        func(b *attachmentBinding) { b.Binding.ChildName = "" },
		"missing-bootstrap":    func(b *attachmentBinding) { b.Binding.BootstrapName = "" },
		"wrong-bootstrap":      func(b *attachmentBinding) { b.Binding.BootstrapName = "another" },
		"invalid-child":        func(b *attachmentBinding) { b.Binding.ChildName = "invalid/name" },
		"missing-profile":      func(b *attachmentBinding) { b.Binding.ProfileName = "" },
		"missing-provider":     func(b *attachmentBinding) { b.Binding.Provider = "" },
		"invalid-provider":     func(b *attachmentBinding) { b.Binding.Provider = "Not_A_Name" },
		"missing-account":      func(b *attachmentBinding) { b.Binding.Account = "" },
		"oversized-region":     func(b *attachmentBinding) { b.Binding.Region = strings.Repeat("r", 64) },
		"profile-uid":          func(b *attachmentBinding) { b.Binding.ProfileUID = "" },
		"infra-uid":            func(b *attachmentBinding) { b.Binding.InfrastructureRevision.UID = "" },
		"infra-generation":     func(b *attachmentBinding) { b.Binding.InfrastructureRevision.Generation = 0 },
		"bootstrap-uid":        func(b *attachmentBinding) { b.Binding.BootstrapRevision.UID = "" },
		"bootstrap-generation": func(b *attachmentBinding) { b.Binding.BootstrapRevision.Generation = -1 },
		"attachment-uid":       func(b *attachmentBinding) { b.UID = "" },
		"cluster-uid":          func(b *attachmentBinding) { b.ClusterUID = "another-cluster" },
		"composite-target": func(b *attachmentBinding) {
			b.Binding.APIGroup = infrav1.GroupVersion.Group
			b.Binding.Kind = compositeProviderKind
		},
	} {
		t.Run(name, func(t *testing.T) {
			cc, bindings := attachmentBindingsFixture()
			change(bindings.Providers["regional"])
			raw, err := json.Marshal(bindings)
			require.NoError(t, err)
			cc.Annotations = map[string]string{infrav1.ProviderAttachmentsAnnotation: string(raw)}
			_, err = readAttachmentBindings(cc)
			require.ErrorIs(t, err, errInvalidComposition)
		})
	}
}

func TestAttachmentFailureRetainsBoundFailureDomains(t *testing.T) {
	_, bindings := attachmentBindingsFixture()
	bindings.Providers["regional"].Zones = []string{"zone-a"}
	plan := &compositionPlan{attachments: &attachmentPlan{bindings: bindings}}
	before, err := plan.allFailureDomains()
	require.NoError(t, err)
	require.Len(t, before, 1)

	plan.attachments.failure = &attachmentFailure{reason: infrav1.ProfileUnavailableReason, cause: errProfileUnavailable}
	after, err := plan.allFailureDomains()
	require.NoError(t, err)
	assert.Equal(t, before, after, "failed expansion must not erase placement from durable bindings")
}

func TestAttachmentBindingsRejectUnknownAndTrailingData(t *testing.T) {
	cc, bindings := attachmentBindingsFixture()
	raw, err := encodeAttachmentBindings(cc, bindings)
	require.NoError(t, err)
	for _, invalid := range []string{
		raw + " {}", strings.Replace(raw, `"profileUID":`, `"profileFingerprint":"SECRET-CANARY","profileUID":`, 1),
		`{"version":2,"providers":{}}`, `{"version":1,"providers":null}`, `null`,
	} {
		cc.Annotations = map[string]string{infrav1.ProviderAttachmentsAnnotation: invalid}
		_, err := readAttachmentBindings(cc)
		require.ErrorIs(t, err, errInvalidComposition)
		assert.NotContains(t, err.Error(), "SECRET-CANARY")
	}
}

func TestPersistAttachmentBindingsRoundTripAndNoOp(t *testing.T) {
	cc, bindings := attachmentBindingsFixture()
	scheme := runtime.NewScheme()
	require.NoError(t, infrav1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cc).Build()
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cc), cc))
	r := &CompositeClusterReconciler{Client: c}
	plan := &attachmentPlan{bindings: bindings}
	require.NoError(t, r.persistCompositionBindings(t.Context(), cc, plan))
	decoded, err := readAttachmentBindings(cc)
	require.NoError(t, err)
	assert.Equal(t, bindings, decoded)
	assert.Contains(t, cc.Finalizers, infrav1.ClusterFinalizer)
	before := cc.DeepCopy()
	require.NoError(t, r.persistCompositionBindings(t.Context(), cc, plan))
	assert.Equal(t, before, cc)
	bindings.Providers["regional"].Binding.ProfileUID = ""
	require.ErrorIs(t, r.persistCompositionBindings(t.Context(), cc, plan), errInvalidComposition)
	assert.Equal(t, before, cc, "invalid records must fail before metadata mutation")
	stored := &infrav1.CompositeCluster{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cc), stored))
	assert.Equal(t, before, stored)
}

func TestAttachmentIdentities(t *testing.T) {
	for _, scenario := range []string{"valid", "collision", "missing-canonical", "retired-canonical"} {
		t.Run(scenario, func(t *testing.T) {
			cc, bindings := attachmentBindingsFixture()
			regional := *bindings.Providers["regional"]
			regional.UID = "other-attachment"
			regional.Binding.ChildName = cc.Name + "-other"
			regional.Binding.BootstrapName = cc.Name + "-other"
			bindings.Providers["other"] = &regional
			switch scenario {
			case "collision":
				regional.Binding.ChildName = cc.Name
			case "missing-canonical":
				delete(bindings.Providers, "regional")
			case "retired-canonical":
				bindings.Providers["regional"].Retired = true
			}
			err := validateAttachmentIdentities(cc, bindings)
			if scenario == "valid" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errInvalidComposition)
			}
		})
	}
}
