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

package mint_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/mint"
)

// The fixtures mirror the spanning-poc (2026-09-02/03): an Outscale + OpenStack
// cluster under one Kamaji control plane. The payloads are trimmed but keep the
// fields that made the PoC work — the endpoint paths OpenStack needs beyond the
// contract field, and the per-provider provider-id overlay whose format must
// match each provider's Machine.spec.providerID.

func spanningCluster() (*infrav1.CompositeCluster, []infrav1.ProviderSpec) {
	cc := &infrav1.CompositeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spanning", Namespace: "tenant-ns"},
		Spec: infrav1.CompositeClusterSpec{
			Bootstrap: json(`{
				"joinConfiguration": {
					"nodeRegistration": {
						"kubeletExtraArgs": [{"name": "cloud-provider", "value": "external"}]
					}
				},
				"preKubeadmCommands": ["hostnamectl set-hostname \"{{ ds.meta_data.local_hostname }}\""]
			}`),
		},
	}
	return cc, []infrav1.ProviderSpec{
		{
			Name:      "outscale",
			Region:    "cloudgouv-eu-west-1",
			InfraSpec: *json(`{"network":{"clusterName":"spanning","net":{"ipRange":"10.0.0.0/16"},"loadBalancer":{"loadbalancername":"spanning-lb"}}}`),
			Bootstrap: json(`{
						"joinConfiguration": {
							"nodeRegistration": {
								"kubeletExtraArgs": [{"name": "provider-id", "value": "aws:///{{ ds.meta_data.placement.availability_zone }}/{{ ds.meta_data.instance_id }}"}]
							}
						}
					}`),
		},
		{
			Name:      "openstack",
			Region:    "GRA11",
			InfraSpec: *json(`{"identityRef":{"name":"ovh-creds","cloudName":"ovh"},"disableAPIServerFloatingIP":true}`),
			Bootstrap: json(`{
						"joinConfiguration": {
							"nodeRegistration": {
								"kubeletExtraArgs": [{"name": "provider-id", "value": "openstack:///{{ instance_id }}"}]
							}
						},
						"preKubeadmCommands": ["/opt/install-kubeadm.sh"]
					}`),
			EndpointPaths: []string{
				"spec.controlPlaneEndpoint",
				"spec.apiServerFixedIP",
				"spec.apiServerPort",
			},
		},
	}
}

func TestRenderInfraCluster_PayloadIsVerbatim(t *testing.T) {
	t.Parallel()

	cc, providers := spanningCluster()
	gvk := schema.GroupVersionKind{
		Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1", Kind: "OscCluster",
	}

	got, err := mint.RenderInfraCluster(cc, &providers[0], gvk)
	require.NoError(t, err)

	assert.Equal(t, map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1",
		"kind":       "OscCluster",
		"metadata": map[string]any{
			// Name-aligned to the CompositeCluster: upstream machine
			// controllers resolve the InfraCluster by name, ignoring kind.
			"name":      "spanning",
			"namespace": "tenant-ns",
			"labels": map[string]any{
				infrav1.ProviderNameLabel: "outscale",
			},
		},
		"spec": map[string]any{
			"network": map[string]any{
				"clusterName":  "spanning",
				"net":          map[string]any{"ipRange": "10.0.0.0/16"},
				"loadBalancer": map[string]any{"loadbalancername": "spanning-lb"},
			},
		},
	}, got.Object)
}

func TestRenderInfraCluster_SiblingsShareTheName(t *testing.T) {
	t.Parallel()

	// The whole point of the delegator: two same-named, different-kind children
	// under one Cluster, so either provider's machines resolve "their"
	// InfraCluster.
	cc, providers := spanningCluster()

	osc, err := mint.RenderInfraCluster(cc, &providers[0], schema.GroupVersionKind{
		Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1", Kind: "OscCluster",
	})
	require.NoError(t, err)

	ostack, err := mint.RenderInfraCluster(cc, &providers[1], schema.GroupVersionKind{
		Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1", Kind: "OpenStackCluster",
	})
	require.NoError(t, err)

	assert.Equal(t, osc.GetName(), ostack.GetName())
	assert.NotEqual(t, osc.GetKind(), ostack.GetKind())
}

func TestRenderInfraCluster_RejectsMalformedPayload(t *testing.T) {
	t.Parallel()

	cc, providers := spanningCluster()
	providers[0].InfraSpec = *json(`["not","an","object"]`)

	_, err := mint.RenderInfraCluster(cc, &providers[0], schema.GroupVersionKind{
		Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1", Kind: "OscCluster",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "infraSpec")
}

func TestClusterBootstrap_CascadePerProvider(t *testing.T) {
	t.Parallel()

	cc, providers := spanningCluster()

	osc, err := mint.ClusterBootstrap(cc, &providers[0])
	require.NoError(t, err)

	ostack, err := mint.ClusterBootstrap(cc, &providers[1])
	require.NoError(t, err)

	// The overlay's provider-id merges INTO the shared kubeletExtraArgs rather
	// than replacing them: v1beta2's list is name-keyed, so both lines must
	// coexist, the shared one first.
	assert.Equal(t, map[string]string{
		"cloud-provider": "external",
		"provider-id":    "aws:///{{ ds.meta_data.placement.availability_zone }}/{{ ds.meta_data.instance_id }}",
	}, kubeletArgs(t, osc))

	assert.Equal(t, "openstack:///{{ instance_id }}", kubeletArgs(t, ostack)["provider-id"])

	// The documented RFC 7386 sharp edge, asserted on the real payloads: the
	// OpenStack overlay's preKubeadmCommands REPLACE the shared list wholesale.
	cmds, _, err := unstructured.NestedSlice(ostack, "preKubeadmCommands")
	require.NoError(t, err)
	assert.Equal(t, []any{"/opt/install-kubeadm.sh"}, cmds)

	// The Outscale overlay does not touch preKubeadmCommands, so the shared
	// list survives there.
	cmds, _, err = unstructured.NestedSlice(osc, "preKubeadmCommands")
	require.NoError(t, err)
	assert.Len(t, cmds, 1)
	assert.Contains(t, cmds[0], "hostnamectl")
}

func TestClusterBootstrap_NilPayloadsAreEmpty(t *testing.T) {
	t.Parallel()

	// The cascade tolerates a cluster with no shared payload and a provider
	// with no overlay: minting must not force either to exist.
	cc, providers := spanningCluster()
	cc.Spec.Bootstrap = nil
	providers[0].Bootstrap = nil

	got, err := mint.ClusterBootstrap(cc, &providers[0])
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestRenderClusterKCT_Golden(t *testing.T) {
	t.Parallel()

	cc, providers := spanningCluster()
	cc.Spec.Bootstrap = json(`{"format":"cloud-config"}`)
	providers[0].Bootstrap = json(
		`{"joinConfiguration":{"nodeRegistration":{"kubeletExtraArgs":[{"name":"provider-id","value":"aws:///x/y"}]}}}`,
	)

	got, err := mint.RenderClusterKCT(cc, &providers[0])
	require.NoError(t, err)

	assert.Equal(t, map[string]any{
		"apiVersion": "bootstrap.cluster.x-k8s.io/v1beta2",
		"kind":       "KubeadmConfigTemplate",
		"metadata": map[string]any{
			// Stable, hash-free name: this template is updated in place, it
			// never rotates.
			"name":      "spanning-outscale",
			"namespace": "tenant-ns",
			"labels": map[string]any{
				infrav1.ProviderNameLabel: "outscale",
			},
		},
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"format": "cloud-config",
					"joinConfiguration": map[string]any{
						"nodeRegistration": map[string]any{
							"kubeletExtraArgs": []any{map[string]any{"name": "provider-id", "value": "aws:///x/y"}},
						},
					},
				},
			},
		},
	}, got.Object)
}

func TestWriteEndpointPaths(t *testing.T) {
	t.Parallel()

	ep := infrav1.APIEndpoint{Host: "203.0.113.10", Port: 6443}

	tests := map[string]struct {
		paths []string
		want  map[string]any
	}{
		"default is the contract field, object form": {
			paths: nil,
			want: map[string]any{
				"controlPlaneEndpoint": map[string]any{"host": "203.0.113.10", "port": int64(6443)},
			},
		},
		"the OpenStack trio from the PoC": {
			paths: []string{"spec.controlPlaneEndpoint", "spec.apiServerFixedIP", "spec.apiServerPort"},
			want: map[string]any{
				"controlPlaneEndpoint": map[string]any{"host": "203.0.113.10", "port": int64(6443)},
				"apiServerFixedIP":     "203.0.113.10",
				"apiServerPort":        int64(6443),
			},
		},
		"a Host-suffixed path receives the host scalar": {
			paths: []string{"spec.apiServerHost"},
			want:  map[string]any{"apiServerHost": "203.0.113.10"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}
			require.NoError(t, mint.WriteEndpointPaths(obj, tc.paths, ep))
			assert.Equal(t, tc.want, obj.Object["spec"])
		})
	}
}

func TestWriteEndpointPaths_DoesNotClobberSiblings(t *testing.T) {
	t.Parallel()

	// Fan-out writes into a spec the provider payload already populated; it
	// must add its fields, not replace the map around them.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"disableAPIServerFloatingIP": true},
	}}

	err := mint.WriteEndpointPaths(obj, []string{"spec.apiServerFixedIP"}, infrav1.APIEndpoint{Host: "h", Port: 1})
	require.NoError(t, err)

	assert.Equal(t, map[string]any{
		"disableAPIServerFloatingIP": true,
		"apiServerFixedIP":           "h",
	}, obj.Object["spec"])
}

// kubeletArgs reads a v1beta2 bootstrap spec's kubeletExtraArgs list back as
// a name→value map, for readable assertions.
func kubeletArgs(t *testing.T, spec map[string]any) map[string]string {
	t.Helper()

	list, _, err := unstructured.NestedSlice(spec, "joinConfiguration", "nodeRegistration", "kubeletExtraArgs")
	require.NoError(t, err)

	out := map[string]string{}

	for _, e := range list {
		m, ok := e.(map[string]any)
		require.True(t, ok, "kubeletExtraArgs entries are objects")
		out[m["name"].(string)], _ = m["value"].(string)
	}

	return out
}

func TestFailureDomains_ProviderNamesVerbatim(t *testing.T) {
	t.Parallel()

	// A MachineDeployment's failureDomain reaches the provider unchanged
	// (CAPOSC: the subregion; CAPO: the availability zone), so the published
	// names must be the providers' own — never prefixed, never derived.
	_, providers := spanningCluster()
	providers[0].Zones = []string{"cloudgouv-eu-west-1a", "cloudgouv-eu-west-1b"}
	providers[1].Zones = []string{"nova"}

	got, err := mint.FailureDomains(providers)
	require.NoError(t, err)

	names := make([]string, 0, len(got))
	for _, fd := range got {
		names = append(names, fd.Name)
	}

	assert.Equal(t, []string{"cloudgouv-eu-west-1a", "cloudgouv-eu-west-1b", "nova"}, names)
	assert.Equal(t, map[string]string{"provider": "outscale", "region": "cloudgouv-eu-west-1", "zone": "cloudgouv-eu-west-1a"}, got[0].Attributes)
	assert.Equal(t, "openstack", got[2].Attributes["provider"])
	assert.False(t, *got[0].ControlPlane, "control planes are Kamaji-hosted, never on tenant infrastructure")

	// A zoneless provider publishes nothing rather than an invented name.
	providers[0].Zones = nil
	providers[1].Zones = nil
	got, err = mint.FailureDomains(providers)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFailureDomains_RejectsInvalidLegacyComposition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*[]infrav1.ProviderSpec)
	}{
		{"duplicate-zone-same-provider", func(ps *[]infrav1.ProviderSpec) { (*ps)[0].Zones = []string{"nova", "nova"} }},
		{"duplicate-zone-conflicting-attributes", func(ps *[]infrav1.ProviderSpec) {
			(*ps)[0].Zones, (*ps)[1].Zones = []string{"nova"}, []string{"nova"}
		}},
		{"duplicate-provider-identical-attributes", func(ps *[]infrav1.ProviderSpec) {
			(*ps)[1] = (*ps)[0]
		}},
		{"duplicate-provider-conflicting-region", func(ps *[]infrav1.ProviderSpec) {
			(*ps)[1].Name = (*ps)[0].Name
		}},
		{"empty-zone", func(ps *[]infrav1.ProviderSpec) { (*ps)[0].Zones = []string{""} }},
		{"overlong-zone", func(ps *[]infrav1.ProviderSpec) { (*ps)[0].Zones = []string{strings.Repeat("z", 257)} }},
		{"17-zones", func(ps *[]infrav1.ProviderSpec) {
			(*ps)[0].Zones = nil
			for i := range 17 {
				(*ps)[0].Zones = append((*ps)[0].Zones, fmt.Sprintf("zone-%d", i))
			}
		}},
		{"101-domains", func(ps *[]infrav1.ProviderSpec) { *ps = failureDomainProviders(101) }},
		{"legacy-provider-overflow", func(ps *[]infrav1.ProviderSpec) {
			*ps = failureDomainProviders(100)
			*ps = append(*ps, infrav1.ProviderSpec{Name: "extra", Region: "region"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, providers := spanningCluster()
			providers[0].Zones = []string{"first"}
			tc.edit(&providers)
			before := make([]infrav1.ProviderSpec, len(providers))
			for i := range providers {
				providers[i].DeepCopyInto(&before[i])
			}
			got, err := mint.FailureDomains(providers)
			require.ErrorIs(t, err, mint.ErrInvalidFailureDomains)
			assert.Empty(t, got, "invalid legacy input must never publish a partial or first-wins result")
			assert.Equal(t, before, providers)
		})
	}
}

func TestFailureDomains_Boundaries(t *testing.T) {
	t.Parallel()
	providers := failureDomainProviders(100)
	got, err := mint.FailureDomains(providers)
	require.NoError(t, err)
	require.Len(t, got, 100)
	for _, fd := range got {
		assert.Equal(t, fd.Name, fd.Attributes["zone"])
		assert.Equal(t, "region", fd.Attributes["region"])
		assert.Equal(t, new(false), fd.ControlPlane)
	}
	providers = providers[:1]
	providers[0].Zones[0] = strings.Repeat("\u754c", 256)
	got, err = mint.FailureDomains(providers)
	require.NoError(t, err)
	require.Len(t, got, 16)
	assert.Equal(t, providers[0].Zones[0], got[0].Name, "native zone length follows schema characters, not bytes")
}

func TestFailureDomains_EmptyComposition(t *testing.T) {
	t.Parallel()
	for _, providers := range [][]infrav1.ProviderSpec{nil, {}} {
		got, err := mint.FailureDomains(providers)
		require.NoError(t, err)
		assert.Empty(t, got, "no infrastructure attachments publish no failure domains")
	}
}

func failureDomainProviders(total int) []infrav1.ProviderSpec {
	providers := make([]infrav1.ProviderSpec, 8)
	for i := range providers {
		providers[i] = infrav1.ProviderSpec{Name: infrav1.ProviderName(fmt.Sprintf("provider-%d", i)), Region: "region"}
		for j := 0; j < 16 && total > 0; j++ {
			providers[i].Zones = append(providers[i].Zones, fmt.Sprintf("native-%d-%d", i, j))
			total--
		}
	}
	return providers
}
