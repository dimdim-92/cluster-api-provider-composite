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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/capi"
	"github.com/numspot/cluster-api-provider-composite/internal/mint"
)

// These tests pin the validation model end to end: a minted child's
// opaque payload is judged by the TARGET CRD's schema via server-side dry-run,
// with no provider knowledge in this component. FakeMachineTemplate is the
// stand-in provider CRD with a typed schema (vmType required string,
// diskSizeGiB integer >= 10).

var fakeMachineTemplateGVK = schema.GroupVersionKind{
	Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1", Kind: "FakeMachineTemplate",
}

// renderFake mints an object of the TYPED stand-in kind from an opaque
// payload — the exact path the reconciler takes before validating a child.
// FakeMachineTemplate is used purely for its typed schema: the payload is
// its spec.template.spec.
func renderFake(t *testing.T, name, payload string) *unstructured.Unstructured {
	t.Helper()

	cc := &infrav1.CompositeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
	}

	obj, err := mint.RenderInfraCluster(cc, &infrav1.ProviderSpec{
		Name: "fake", Region: "reg", InfraSpec: rawJSON(`{"template":{"spec":` + payload + `}}`),
	}, fakeMachineTemplateGVK)
	require.NoError(t, err)

	return obj
}

func TestDryRunChild_AcceptsAValidPayload(t *testing.T) {
	obj := renderFake(t, "dryrun-valid", `{"vmType":"tinav7.c4r8p2","diskSizeGiB":30}`)

	require.NoError(t, capi.DryRunChild(ctx(t), k8s, obj))

	// Dry-run means dry: nothing may have been persisted.
	err := k8s.Get(ctx(t), types.NamespacedName{
		Name: obj.GetName(), Namespace: "default",
	}, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1",
		"kind":       "FakeMachineTemplate",
	}})
	assert.True(t, apierrors.IsNotFound(err), "dry-run must not persist the child")
}

func TestDryRunChild_SurfacesSchemaViolations(t *testing.T) {
	tests := map[string]struct {
		payload string
		wantErr string
	}{
		"type mismatch names the field": {
			payload: `{"vmType":42}`,
			wantErr: "vmType",
		},
		"missing required field": {
			payload: `{"diskSizeGiB":30}`,
			wantErr: "vmType",
		},
		"bounds violation": {
			payload: `{"vmType":"tinav7.c4r8p2","diskSizeGiB":5}`,
			wantErr: "diskSizeGiB",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			obj := renderFake(t, "dryrun-"+sanitise(name), tc.payload)

			err := capi.DryRunChild(ctx(t), k8s, obj)
			require.Error(t, err, "the target CRD's schema must reject this payload")
			assert.Contains(t, err.Error(), tc.wantErr,
				"the error must carry the field path so it is actionable in a condition")
			// Deliberately IsPayloadRejection and not IsInvalid: a type
			// mismatch dies inside SSA's typed-patch conversion as a 500,
			// before the validator would return 422 Invalid. This table covers
			// both paths, and the helper is what reconcilers must branch on.
			assert.True(t, capi.IsPayloadRejection(err),
				"the reconciler must be able to tell payload bugs from transport errors")
		})
	}
}

func TestDryRunChild_DoesNotMutateTheRenderedObject(t *testing.T) {
	obj := renderFake(t, "dryrun-nomutate", `{"vmType":"tinav7.c4r8p2"}`)
	before := obj.DeepCopy()

	require.NoError(t, capi.DryRunChild(ctx(t), k8s, obj))
	assert.Equal(t, before.Object, obj.Object,
		"the API server's defaulted echo must not leak into the mint's output")
}
