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

package mint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

// The kubeadm bootstrap provider's template kind and version. ONE wire
// version, v1beta2, for every CAPI object this provider writes: payloads are
// authored in the v1beta2 shape (kubeletExtraArgs as a list of {name, value}),
// and the reconcilers refuse to start on a core that does not serve it.
// Writing at a version the server merely converts is never a no-op — see
// docs/design.md for the served-version constraint.
const (
	KubeadmConfigTemplateAPIVersion = "bootstrap.cluster.x-k8s.io/v1beta2"
	KubeadmConfigTemplateKind       = "KubeadmConfigTemplate"

	specField = "spec"
)

// newChildObject assembles a minted object: type, metadata, spec. Every
// object this package renders goes through here so the metadata shape cannot
// drift between kinds.
func newChildObject(
	apiVersion, kind, name, namespace string,
	labels map[string]string,
	spec map[string]any,
) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{specField: spec}}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	obj.SetLabels(labels)

	return obj
}

// templateWrap nests a payload as a template kind's spec.template.spec — the
// shape MachineTemplates and KubeadmConfigTemplates share.
func templateWrap(payload map[string]any) map[string]any {
	return map[string]any{"template": map[string]any{specField: payload}}
}

var errTrailingPayload = errors.New("payload must contain a single JSON value")

// payloadMap decodes an opaque payload without rounding its numbers. A nil payload
// is an empty map: the cascade tolerates a missing shared base or overlay.
func payloadMap(j *apiextensionsv1.JSON) (map[string]any, error) {
	if j == nil || len(j.Raw) == 0 {
		return map[string]any{}, nil
	}

	out := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(j.Raw))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding payload: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errTrailingPayload
	}

	return out, nil
}

// RenderInfraCluster renders one provider's InfraCluster child from its opaque
// infraSpec payload, verbatim. The child uses the attachment's native name;
// ownerRefs are the caller's job through capi.ApplyChild.
func RenderInfraCluster(
	cc *infrav1.CompositeCluster,
	p *infrav1.ProviderSpec,
	gvk schema.GroupVersionKind,
) (*unstructured.Unstructured, error) {
	spec, err := payloadMap(&p.InfraSpec)
	if err != nil {
		return nil, fmt.Errorf("provider %q infraSpec: %w", p.Name, err)
	}

	return RenderInfraClusterSpec(cc, p.Name, gvk, ClusterInfraName(cc, p), spec), nil
}

// ClusterInfraName preserves the canonical lookup name unless a profile-backed
// attachment explicitly selects an additional regional infrastructure object.
func ClusterInfraName(cc *infrav1.CompositeCluster, p *infrav1.ProviderSpec) string {
	if p.InfraClusterName != "" {
		return p.InfraClusterName
	}
	return cc.Name
}

// RenderInfraClusterSpec renders already-resolved configuration without another
// numeric decode or merge. Its payload is independent of the caller's map.
func RenderInfraClusterSpec(
	cc *infrav1.CompositeCluster,
	provider infrav1.ProviderName,
	gvk schema.GroupVersionKind,
	name string,
	spec map[string]any,
) *unstructured.Unstructured {
	return newChildObject(
		gvk.GroupVersion().String(), gvk.Kind,
		name, cc.Namespace,
		map[string]string{
			infrav1.ProviderNameLabel: string(provider),
		},
		runtime.DeepCopyJSON(spec),
	)
}

// ClusterBootstrap is the bootstrap cascade for one provider:
// spec.bootstrap ⊕ providers[].bootstrap, RFC 7386.
func ClusterBootstrap(cc *infrav1.CompositeCluster, p *infrav1.ProviderSpec) (map[string]any, error) {
	base, err := payloadMap(cc.Spec.Bootstrap)
	if err != nil {
		return nil, fmt.Errorf("spec.bootstrap: %w", err)
	}

	merged, err := MergePatch(base, p.Bootstrap)
	if err != nil {
		return nil, fmt.Errorf("provider %q bootstrap overlay: %w", p.Name, err)
	}

	return merged, nil
}

// ClusterKCTName is the stable name of a provider's minted template.
func ClusterKCTName(cc *infrav1.CompositeCluster, p *infrav1.ProviderSpec) string {
	return cc.Name + "-" + string(p.Name)
}

// RenderClusterKCT renders the stable per-provider KubeadmConfigTemplate from
// the bootstrap cascade. It is updated in place on payload changes — the
// kubeadm bootstrap provider reads it at machine creation, so changes affect
// new machines only. Mint it only after EndpointPropagated: join commands
// render from the settled endpoint.
func RenderClusterKCT(cc *infrav1.CompositeCluster, p *infrav1.ProviderSpec) (*unstructured.Unstructured, error) {
	cascade, err := ClusterBootstrap(cc, p)
	if err != nil {
		return nil, err
	}

	return RenderClusterKCTSpec(cc, p.Name, ClusterKCTName(cc, p), cascade), nil
}

// RenderClusterKCTSpec wraps an already-composed bootstrap payload. It does not
// reapply a profile or consumer overlay and preserves exact JSON numbers.
func RenderClusterKCTSpec(cc *infrav1.CompositeCluster, provider infrav1.ProviderName, name string, spec map[string]any) *unstructured.Unstructured {
	return newChildObject(
		KubeadmConfigTemplateAPIVersion, KubeadmConfigTemplateKind,
		name, cc.Namespace,
		map[string]string{
			infrav1.ProviderNameLabel: string(provider),
		},
		templateWrap(runtime.DeepCopyJSON(spec)),
	)
}

// WriteEndpointPaths fans the control plane endpoint out into a rendered child
// at the provider's declared paths. Path semantics (data replaces per-provider
// code):
//
//   - a path whose last segment ends in "port" receives the port scalar;
//   - a path whose last segment ends in "ip" or "host" receives the host;
//   - any other path receives the object form {host, port}.
//
// Suffixes match case-insensitively.
//
// An empty list defaults to ["spec.controlPlaneEndpoint"].
func WriteEndpointPaths(obj *unstructured.Unstructured, paths []string, ep infrav1.APIEndpoint) error {
	if len(paths) == 0 {
		paths = []string{"spec.controlPlaneEndpoint"}
	}

	for _, path := range paths {
		fields := strings.Split(path, ".")
		// Case-insensitive: "spec.controlPlaneEndpoint.host" and CAPO's
		// "apiServerFixedIP" both name scalars.
		last := strings.ToLower(fields[len(fields)-1])

		var err error
		switch {
		case strings.HasSuffix(last, "port"):
			err = unstructured.SetNestedField(obj.Object, int64(ep.Port), fields...)
		case strings.HasSuffix(last, "ip"), strings.HasSuffix(last, "host"):
			err = unstructured.SetNestedField(obj.Object, ep.Host, fields...)
		default:
			err = unstructured.SetNestedMap(obj.Object, map[string]any{
				"host": ep.Host,
				"port": int64(ep.Port),
			}, fields...)
		}

		if err != nil {
			return fmt.Errorf("writing endpoint at %q: %w", path, err)
		}
	}

	return nil
}

// FailureDomains publishes the contract's failure domains: the UNION of every
// provider's zones, each name VERBATIM. A MachineDeployment's
// spec.failureDomain reaches the provider unchanged (CAPOSC uses it as the
// subregion, CAPO as the availability zone), so the only usable names are the
// providers' own; the payload author supplies them and the attributes say
// which cloud each belongs to. A zoneless provider publishes nothing. The
// composition must not contain duplicate names. Never suitable for control planes:
// those are Kamaji-hosted.
//
// The list shape is the v1beta2 contract's. A v1beta1-era core would read
// this field as a map and fail; the cores this deploys to are v1.12+.
func FailureDomains(providers []infrav1.ProviderSpec) ([]infrav1.FailureDomain, error) {
	var out []infrav1.FailureDomain
	seen := map[string]bool{}
	if err := validateDomainProviders(providers); err != nil {
		return nil, err
	}
	for i := range providers {
		p := &providers[i]
		for _, zone := range p.Zones {
			if seen[zone] || zone == "" || utf8.RuneCountInString(zone) > 256 || len(out) == 100 {
				return nil, fmt.Errorf("provider %q has duplicate/invalid zones or exceeds 100 total domains: %w", p.Name, ErrInvalidFailureDomains)
			}

			seen[zone] = true

			out = append(out, infrav1.FailureDomain{
				Name:         zone,
				ControlPlane: new(false),
				Attributes: map[string]string{
					"provider": string(p.Name),
					"region":   p.Region,
					"zone":     zone,
				},
			})
		}
	}

	return out, nil
}

func validateDomainProviders(providers []infrav1.ProviderSpec) error {
	if len(providers) > 8 {
		return fmt.Errorf("expected at most 8 providers: %w", ErrInvalidFailureDomains)
	}
	seen := map[infrav1.ProviderName]bool{}
	for i := range providers {
		p := &providers[i]
		if seen[p.Name] || p.Name == "" || len(p.Zones) > 16 {
			return fmt.Errorf("provider %q is duplicate, unnamed or exceeds 16 zones: %w", p.Name, ErrInvalidFailureDomains)
		}
		seen[p.Name] = true
	}
	return nil
}

// ErrInvalidFailureDomains prevents legacy input from publishing a partial or ambiguous zone union.
var ErrInvalidFailureDomains = errors.New("invalid failure domains")
