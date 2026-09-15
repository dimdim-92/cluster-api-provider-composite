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
	"slices"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func (r *CompositeClusterReconciler) profileToComposites(ctx context.Context, obj client.Object) []reconcile.Request {
	p, ok := obj.(*infrav1.CompositeProviderTemplate)
	if !ok {
		return nil
	}
	var requests []reconcile.Request
	attachments := &infrav1.CompositeProviderList{}
	if err := r.List(ctx, attachments); err != nil {
		ctrl.LoggerFrom(ctx).Error(safeReconcileError(err), "Unable to list provider attachments; polling will retry")
		return requests
	}
	for i := range attachments.Items {
		attachment := &attachments.Items[i]
		if attachment.Spec.TemplateRef.Name == p.Name {
			request := reconcile.Request{NamespacedName: client.ObjectKey{Namespace: attachment.Namespace, Name: attachment.Spec.ClusterRef.Name}}
			if !slices.Contains(requests, request) {
				requests = append(requests, request)
			}
		}
	}
	return requests
}
