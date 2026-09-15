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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// compositionCRD returns an uninstalled, uniquely named clone; shared fixture CRDs are never changed.
func compositionCRD(t *testing.T, namespace string) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	crd := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, k8s.Get(t.Context(), client.ObjectKey{Name: "fakeclusters.infrastructure.cluster.x-k8s.io"}, crd))
	kind := "Composition" + strings.ReplaceAll(namespace, "-", "")
	plural := strings.ToLower(kind) + "s"
	crd.ObjectMeta = metav1.ObjectMeta{Name: plural + "." + crd.Spec.Group, Labels: map[string]string{
		"cluster.x-k8s.io/v1beta1": "v1beta1",
	}}
	crd.Spec.Names = apiextensionsv1.CustomResourceDefinitionNames{Kind: kind, ListKind: kind + "List", Plural: plural}
	crd.Status = apiextensionsv1.CustomResourceDefinitionStatus{}
	return crd
}
