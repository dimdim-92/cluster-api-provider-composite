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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func TestProfileWatch_OnlySelectedConsumers(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, infrav1.AddToScheme(scheme))
	attachment := func(name, cluster, template string) *infrav1.CompositeProvider {
		return &infrav1.CompositeProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "consumers"},
			Spec: infrav1.CompositeProviderSpec{
				ClusterRef:  infrav1.CompositeClusterReference{Name: cluster},
				TemplateRef: infrav1.CompositeProviderReference{Name: template},
			},
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		attachment(
			"first",
			"selected",
			"profile-a",
		), attachment("second", "selected", "profile-a"), attachment("other", "other", "profile-b")).
		Build()
	r := &CompositeClusterReconciler{Client: c}
	p := &infrav1.CompositeProviderTemplate{ObjectMeta: metav1.ObjectMeta{Name: "profile-a"}}
	assert.Equal(
		t,
		[]ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: "consumers", Name: "selected"}}},
		r.profileToComposites(t.Context(), p),
	)
	assert.Empty(t, r.profileToComposites(t.Context(), &infrav1.CompositeCluster{}))
}
