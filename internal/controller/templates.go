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
	"context"
	"encoding/json"
	"errors"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/mint"
	"github.com/numspot/cluster-api-provider-composite/internal/profile"
)

var (
	errTemplateUnavailable = errors.New("source template is unavailable or deleting")
	errInvalidTemplate     = errors.New("source template reference or payload is invalid")
)

type profileTemplates struct {
	configuration  profile.Configuration
	infrastructure sourceRevision
	bootstrap      sourceRevision
}

func resolveProfileTemplates(ctx context.Context, reader client.Reader, catalog *infrav1.CompositeProviderTemplate) (*profileTemplates, error) {
	spec := &catalog.Spec
	gv, err := schema.ParseGroupVersion(spec.InfrastructureTemplateRef.APIVersion)
	if err != nil || gv.Group != spec.InfraClusterKind.APIGroup || spec.InfrastructureTemplateRef.Kind != spec.InfraClusterKind.Kind+"Template" ||
		isCompositeKind(spec.InfraClusterKind.APIGroup, spec.InfraClusterKind.Kind) ||
		spec.BootstrapTemplateRef.APIVersion != mint.KubeadmConfigTemplateAPIVersion || spec.BootstrapTemplateRef.Kind != mint.KubeadmConfigTemplateKind {
		return nil, errInvalidTemplate
	}
	infrastructure, infraRevision, err := readSourceTemplate(ctx, reader, spec.InfrastructureTemplateRef)
	if err != nil {
		return nil, fmt.Errorf("reading infrastructure template: %w", err)
	}
	bootstrap, bootstrapRevision, err := readSourceTemplate(ctx, reader, spec.BootstrapTemplateRef)
	if err != nil {
		return nil, fmt.Errorf("reading bootstrap template: %w", err)
	}
	return &profileTemplates{
		configuration: profile.Configuration{
			Defaults:       &profile.Defaults{InfraSpec: infrastructure, Bootstrap: bootstrap},
			OverridePolicy: spec.OverridePolicy, EndpointPaths: spec.EndpointPaths,
			CredentialsRef: spec.CredentialsRef,
		},
		infrastructure: infraRevision, bootstrap: bootstrapRevision,
	}, nil
}

func readSourceTemplate(
	ctx context.Context,
	reader client.Reader,
	ref infrav1.ProviderTemplateReference,
) (*apiextensionsv1.JSON, sourceRevision, error) {
	gvk, err := sourceTemplateGVK(ref)
	if err != nil {
		return nil, sourceRevision{}, err
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := reader.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, obj); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			err = errors.Join(errTemplateUnavailable, err)
		}
		return nil, sourceRevision{}, fmt.Errorf("observing source template: %w", err)
	}
	revision := revisionOf(obj)
	if !revision.valid() || obj.GetDeletionTimestamp() != nil || obj.GetNamespace() != ref.Namespace {
		return nil, sourceRevision{}, errTemplateUnavailable
	}
	payload, err := sourceTemplatePayload(obj)
	if err != nil {
		return nil, sourceRevision{}, err
	}
	return payload, revision, nil
}

func sourceTemplateGVK(ref infrav1.ProviderTemplateReference) (schema.GroupVersionKind, error) {
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil || gv.Group == "" || gv.Version == "" || ref.Kind == "" ||
		len(validation.IsDNS1123Label(ref.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(ref.Name)) != 0 {
		return schema.GroupVersionKind{}, errInvalidTemplate
	}
	return gv.WithKind(ref.Kind), nil
}

func sourceTemplatePayload(obj *unstructured.Unstructured) (*apiextensionsv1.JSON, error) {
	payload, found, err := unstructured.NestedMap(obj.Object, "spec", "template", "spec")
	if err != nil || !found || payload == nil {
		return nil, errInvalidTemplate
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, errInvalidTemplate
	}
	return &apiextensionsv1.JSON{Raw: encoded}, nil
}
