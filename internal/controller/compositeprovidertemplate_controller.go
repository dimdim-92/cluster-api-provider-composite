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
	"errors"
	"fmt"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/profile"
)

// CompositeProviderTemplateReconciler protects the global catalog independently of the
// namespace/filter scope of cluster reconciliation. It owns no native resources.
type CompositeProviderTemplateReconciler struct {
	client.Client
	APIReader client.Reader
}

// SetupWithManager observes all cluster-scoped profiles. Reference checks below
// deliberately bypass the manager's namespace-scoped informer cache.
func (r *CompositeProviderTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.APIReader = mgr.GetAPIReader()
	if err := ctrl.NewControllerManagedBy(mgr).For(&infrav1.CompositeProviderTemplate{}).Complete(r); err != nil {
		return fmt.Errorf("building CompositeProvider controller: %w", err)
	}
	return nil
}

// Reconcile retains referenced profiles and reports defaults/policy validation.
// Configuration validity is not native infrastructure or worker readiness.
func (r *CompositeProviderTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	ctx = ctrl.LoggerInto(ctx, ctrl.LoggerFrom(ctx).WithValues("compositeProvider", req.Name))
	defer func() { err = safeReconcileError(err) }()
	p := &infrav1.CompositeProviderTemplate{}
	if err := r.reader().Get(ctx, req.NamespacedName, p); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("reading profile: %w", err)
	}
	if !p.DeletionTimestamp.IsZero() {
		return r.reconcileProfileDelete(ctx, p)
	}
	if controllerutil.AddFinalizer(p, infrav1.ProviderInUseFinalizer) {
		if err := r.Update(ctx, p); err != nil {
			return ctrl.Result{}, fmt.Errorf("protecting profile: %w", err)
		}
	}
	if err := r.observeProfile(ctx, p); err != nil {
		return ctrl.Result{}, err
	}
	if !isTrue(p.Status.Conditions, infrav1.ConfigurationValidCondition) {
		return settling, nil
	}
	// Source templates may be outside the manager's namespaced cache.
	return steady, nil
}

func (r *CompositeProviderTemplateReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *CompositeProviderTemplateReconciler) observeProfile(ctx context.Context, p *infrav1.CompositeProviderTemplate) error {
	before := p.DeepCopy()
	status, reason := metav1.ConditionTrue, infrav1.ReadyReason
	message := "Source templates and override policy are valid; this does not establish native infrastructure or worker readiness."
	templates, observationErr := resolveProfileTemplates(ctx, r.reader(), p)
	if observationErr == nil {
		if len(p.Spec.CredentialsNamePaths) != 0 {
			templates.configuration.CredentialsRef = infrav1.ProviderCredentialsReference{
				Name:      "validation-placeholder",
				NamePaths: p.Spec.CredentialsNamePaths,
			}
		}
		_, observationErr = profile.Resolve(&templates.configuration, nil, nil, nil)
	}
	if observationErr != nil {
		status, reason, message = profileConfigurationFailure(observationErr)
	}
	setCondition(&p.Status.Conditions, p.Generation, infrav1.ConfigurationValidCondition, status, reason, message)
	p.Status.ObservedGeneration = p.Generation
	if !reflect.DeepEqual(before.Status, p.Status) {
		if err := r.Status().Patch(ctx, p, client.MergeFrom(before)); err != nil {
			return fmt.Errorf("reporting profile validation: %w", err)
		}
	}
	if observationErr != nil && status == metav1.ConditionUnknown {
		return fmt.Errorf("observing profile sources: %w", observationErr)
	}
	return nil
}

func profileConfigurationFailure(err error) (status metav1.ConditionStatus, reason, message string) {
	switch {
	case errors.Is(err, errTemplateUnavailable), errors.Is(err, errInvalidTemplate):
		return metav1.ConditionFalse, infrav1.ProfileUnavailableReason, "Source template is unavailable or invalid; inspect the referenced templates."
	case errors.Is(err, profile.ErrInvalidCredentials):
		return metav1.ConditionFalse, infrav1.OverrideRejectedReason, "Credential reference is invalid; check its Secret name and native target paths."
	case errors.Is(err, profile.ErrInvalidJSON),
		errors.Is(err, profile.ErrInvalidPolicy),
		errors.Is(err, profile.ErrOverrideDenied),
		errors.Is(err, profile.ErrProtectedOverride):
		return metav1.ConditionFalse, infrav1.OverrideRejectedReason, "Template defaults or override policy are invalid; review the catalog configuration."
	default:
		return metav1.ConditionUnknown, infrav1.WaitingForProvidersReason, "Source templates could not be observed; the controller will retry."
	}
}

func (r *CompositeProviderTemplateReconciler) reconcileProfileDelete(ctx context.Context, p *infrav1.CompositeProviderTemplate) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(p, infrav1.ProviderInUseFinalizer) {
		return ctrl.Result{}, nil
	}
	referenced, err := clustersReferenceProfile(ctx, r.reader(), p.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !referenced {
		referenced, err = attachmentsReferenceTemplate(ctx, r.reader(), p.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	if referenced {
		// Polling also observes consumers outside this manager's watch scope.
		return settling, nil
	}
	controllerutil.RemoveFinalizer(p, infrav1.ProviderInUseFinalizer)
	if err := r.Update(ctx, p); err != nil {
		return ctrl.Result{}, fmt.Errorf("releasing unused profile: %w", err)
	}
	return ctrl.Result{}, nil
}

func clustersReferenceProfile(ctx context.Context, reader client.Reader, name string) (bool, error) {
	continuation := ""
	for {
		list := &infrav1.CompositeClusterList{}
		if err := reader.List(ctx, list, client.Limit(100), client.Continue(continuation)); err != nil {
			return false, fmt.Errorf("checking cluster profile references: %w", err)
		}
		for i := range list.Items {
			referenced, err := clusterReferencesProfile(&list.Items[i], name)
			if err != nil {
				return false, err
			}
			if referenced {
				return true, nil
			}
		}
		if list.Continue == "" {
			return false, nil
		}
		continuation = list.Continue
	}
}

func clusterReferencesProfile(cc *infrav1.CompositeCluster, name string) (bool, error) {
	attachments, err := readAttachmentBindings(cc)
	if err != nil {
		return false, err
	}
	for _, entry := range attachments.Providers {
		if !entry.Retired && entry.Binding.ProfileName == name {
			return true, nil
		}
	}
	return false, nil
}

func attachmentsReferenceTemplate(ctx context.Context, reader client.Reader, name string) (bool, error) {
	continuation := ""
	for {
		list := &infrav1.CompositeProviderList{}
		if err := reader.List(ctx, list, client.Limit(100), client.Continue(continuation)); err != nil {
			return false, fmt.Errorf("checking global provider template references: %w", err)
		}
		for i := range list.Items {
			if list.Items[i].Spec.TemplateRef.Name == name {
				return true, nil
			}
		}
		if list.Continue == "" {
			return false, nil
		}
		continuation = list.Continue
	}
}
