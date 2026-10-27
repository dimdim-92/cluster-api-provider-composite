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
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

// Requeue intervals. Native children use dynamically resolved GVKs without
// dedicated watches, so progress on a child is noticed by polling.
const (
	// requeueSettling is used while anything is still converging.
	requeueSettling = 30 * time.Second

	// requeueSteady is the resync when everything is Ready — cheap drift
	// detection, nothing more.
	requeueSteady = 5 * time.Minute
)

// settling and steady are the two Results a successful reconcile returns.
var (
	settling = ctrl.Result{RequeueAfter: requeueSettling}
	steady   = ctrl.Result{RequeueAfter: requeueSteady}
)

// setCondition writes one condition, stamping the observed generation.
func setCondition(conds *[]metav1.Condition, generation int64, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            clip(message),
		ObservedGeneration: generation,
	})
}

// setTrue marks a condition healthy.
func setTrue(conds *[]metav1.Condition, generation int64, condType string) {
	setCondition(conds, generation, condType, metav1.ConditionTrue, infrav1.ReadyReason, "")
}

// setFalse marks a condition unhealthy with a cloud-neutral reason.
func setFalse(conds *[]metav1.Condition, generation int64, condType, reason, message string) {
	setCondition(conds, generation, condType, metav1.ConditionFalse, reason, message)
}

// isTrue reads a condition back.
func isTrue(conds []metav1.Condition, condType string) bool {
	return apimeta.IsStatusConditionTrue(conds, condType)
}
