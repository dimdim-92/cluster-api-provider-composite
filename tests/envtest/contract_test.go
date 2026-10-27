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

package envtest_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/numspot/cluster-api-provider-composite/internal/capi"
)

var compositeClusterGK = schema.GroupKind{
	Group: "infrastructure.cluster.x-k8s.io",
	Kind:  "CompositeCluster",
}

// TestContractCache_ResolvesOurCRDs is the regression test for the defect that
// makes an infrastructure provider silently inert: a CRD without the
// cluster.x-k8s.io/<contract> label cannot be resolved by CAPI, because in
// v1beta2 Cluster.spec.infrastructureRef carries no version and CAPI reads it
// from that label.
//
// It fails only at runtime, only once a Cluster exists, and produces no error on
// apply -- so it is pinned here, against the CRDs `make gen` actually produces.
func TestContractCache_ResolvesOurCRDs(t *testing.T) {
	c := capi.NewContractCache(k8s)

	got, err := c.Get(ctx(t), compositeClusterGK)
	require.NoError(t, err, "our own CRDs must carry the CAPI contract label")

	assert.Equal(t, capi.ContractV1Beta2, got.Contract)
	assert.Equal(t, "infrastructure.cluster.x-k8s.io/v1beta2", got.APIVersion)
}

// TestContractCache_ReportsMissingCRD checks the path that keeps one image
// deployable to a Service Cluster where only some providers are installed: an
// absent CRD must be distinguishable from a real failure, so the caller can
// disable that translator instead of failing the cluster.
func TestContractCache_ReportsMissingCRD(t *testing.T) {
	c := capi.NewContractCache(k8s)

	_, err := c.Get(ctx(t), schema.GroupKind{
		Group: "infrastructure.cluster.x-k8s.io",
		Kind:  "OpenStackCluster", // CAPO is not installed in this envtest
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, capi.ErrCRDNotInstalled)
}

// TestContractCache_Caches asserts stable resolution for an unchanged CRD.
func TestContractCache_Caches(t *testing.T) {
	c := capi.NewContractCache(k8s)

	first, err := c.Get(ctx(t), compositeClusterGK)
	require.NoError(t, err)

	second, err := c.Get(ctx(t), compositeClusterGK)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestContractCache_ObservesCRDChanges(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, k8s.Get(t.Context(), client.ObjectKey{Name: "fakeclusters.infrastructure.cluster.x-k8s.io"}, crd))
	kind := "Refresh" + strings.ReplaceAll(newNamespace(t), "-", "")
	plural := strings.ToLower(kind) + "s"
	crd.ObjectMeta = metav1.ObjectMeta{Name: plural + "." + crd.Spec.Group, Labels: map[string]string{
		"cluster.x-k8s.io/v1beta1": "v1beta1",
	}}
	crd.Spec.Names = apiextensionsv1.CustomResourceDefinitionNames{Kind: kind, ListKind: kind + "List", Plural: plural}
	crd.Status = apiextensionsv1.CustomResourceDefinitionStatus{}
	require.NoError(t, k8s.Create(t.Context(), crd))
	c := capi.NewContractCache(k8s)
	gk := schema.GroupKind{Group: crd.Spec.Group, Kind: kind}
	first, err := c.Get(t.Context(), gk)
	require.NoError(t, err)
	assert.Equal(t, capi.ContractV1Beta1, first.Contract)
	changeContractLabels(t, crd, func(labels map[string]string) { labels["cluster.x-k8s.io/v1beta2"] = "v1beta1" })
	second, err := c.Get(t.Context(), gk)
	require.NoError(t, err)
	assert.Equal(t, capi.ContractV1Beta2, second.Contract, "a provider upgrade must not require a Composite restart")
	changeContractLabels(t, crd, func(labels map[string]string) {
		delete(labels, "cluster.x-k8s.io/v1beta1")
		labels["cluster.x-k8s.io/v1beta2"] = "v9"
	})
	_, err = c.Get(t.Context(), gk)
	require.Error(t, err, "an unavailable API version must not be used as a successful cached resolution")
}

func changeContractLabels(t *testing.T, crd *apiextensionsv1.CustomResourceDefinition, change func(map[string]string)) {
	t.Helper()
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := k8s.Get(t.Context(), client.ObjectKeyFromObject(crd), crd); err != nil {
			return fmt.Errorf("reading CRD before label update: %w", err)
		}
		change(crd.Labels)
		if err := k8s.Update(t.Context(), crd); err != nil {
			return fmt.Errorf("updating CRD labels: %w", err)
		}
		return nil
	}))
}
