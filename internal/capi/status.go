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

package capi

import (
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

// ErrUnknownContract means a contract version was requested that this package
// does not know how to read.
var ErrUnknownContract = errors.New("unknown contract version")

// ErrInvalidEndpoint means a reported endpoint cannot represent a valid TCP port.
var ErrInvalidEndpoint = errors.New("endpoint port must be between 0 and 65535")

// ChildStatus is the contract-defined subset of a provider object's status,
// read without importing that provider's Go types.
//
// Handling children as unstructured is deliberate: vendoring the CAPOSC and
// CAPO type packages would pull two large, independently versioned module trees
// into the build and pin us to their release cadence, in exchange for typed
// access to about six fields that the contract already fixes.
type ChildStatus struct {
	// Provisioned is the contract's readiness bit, read from whichever path the
	// child's contract version dictates.
	Provisioned bool

	// Ready is current child health, independently of completed initialization.
	Ready bool

	// Endpoint is spec.controlPlaneEndpoint, if the child carries one.
	Endpoint infrav1.APIEndpoint

	// FailureReason and FailureMessage are the deprecated-but-still-populated
	// terminal failure fields. Both current providers write them.
	FailureReason  string
	FailureMessage string
}

// ReadInfraCluster extracts the contract fields from a child InfraCluster.
//
// The provisioned path depends on the child's contract version:
//
//	v1beta1  ->  status.ready
//	v1beta2  ->  status.initialization.provisioned
//
// The earlier prototype read status.ready unconditionally. That is correct for
// today's providers and silently wrong for any v1beta2 one -- readiness would
// read false forever with no error surfaced anywhere.
func ReadInfraCluster(obj *unstructured.Unstructured, contract ContractVersion) (ChildStatus, error) {
	var out ChildStatus

	provisioned, err := readProvisioned(obj, contract)
	if err != nil {
		return out, err
	}

	out.Provisioned = provisioned

	out.Endpoint, err = ReadEndpoint(obj)
	if err != nil {
		return out, err
	}

	out.FailureReason, _, err = unstructured.NestedString(obj.Object, "status", "failureReason")
	if err != nil {
		return out, fmt.Errorf("ReadInfraCluster: reading status.failureReason: %w", err)
	}

	out.FailureMessage, _, err = unstructured.NestedString(obj.Object, "status", "failureMessage")
	if err != nil {
		return out, fmt.Errorf("ReadInfraCluster: reading status.failureMessage: %w", err)
	}
	ready, reason, message, err := readReadyCondition(obj, provisioned)
	if err != nil {
		return out, err
	}
	out.Ready = ready
	if reason != "" || message != "" {
		out.FailureReason, out.FailureMessage = reason, message
	}

	return out, nil
}

// ReadEndpoint reads a partial or complete CAPI endpoint without integer narrowing.
// A missing port remains zero so callers can wait for endpoint initialization.
func ReadEndpoint(obj *unstructured.Unstructured) (infrav1.APIEndpoint, error) {
	host, _, err := unstructured.NestedString(obj.Object, "spec", "controlPlaneEndpoint", "host")
	if err != nil {
		return infrav1.APIEndpoint{}, fmt.Errorf("ReadEndpoint: reading host: %w", err)
	}
	port, _, err := unstructured.NestedInt64(obj.Object, "spec", "controlPlaneEndpoint", "port")
	if err != nil {
		return infrav1.APIEndpoint{}, fmt.Errorf("ReadEndpoint: reading port: %w", err)
	}
	if port < 0 || port > 65535 {
		return infrav1.APIEndpoint{}, ErrInvalidEndpoint
	}
	return infrav1.APIEndpoint{Host: host, Port: int32(port)}, nil
}

func readReadyCondition(obj *unstructured.Unstructured, provisioned bool) (ready bool, reason, message string, err error) {
	conditions, _, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil {
		return false, "", "", fmt.Errorf("ReadInfraCluster: reading conditions: %w", err)
	}
	for _, raw := range conditions {
		value, ok := raw.(map[string]any)
		if !ok || value["type"] != infrav1.ReadyCondition {
			continue
		}
		var condition metav1.Condition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(value, &condition); err != nil {
			return false, "", "", fmt.Errorf("ReadInfraCluster: decoding Ready condition: %w", err)
		}
		if _, reported := value["observedGeneration"]; reported && condition.ObservedGeneration != obj.GetGeneration() {
			return false, "StaleReadyCondition", "child Ready condition has not observed the current generation", nil
		}
		return condition.Status == metav1.ConditionTrue, condition.Reason, condition.Message, nil
	}
	return provisioned, "", "", nil
}

// readProvisioned resolves the contract's readiness bit for either contract
// version. A missing field is reported as not-provisioned rather than an error:
// a freshly created child has no status at all yet, and that is the normal case
// on the first few reconciles.
func readProvisioned(obj *unstructured.Unstructured, contract ContractVersion) (bool, error) {
	switch contract {
	case ContractV1Beta1:
		v, _, err := unstructured.NestedBool(obj.Object, "status", "ready")
		if err != nil {
			return false, fmt.Errorf("readProvisioned: reading status.ready: %w", err)
		}

		return v, nil

	case ContractV1Beta2:
		v, _, err := unstructured.NestedBool(obj.Object, "status", "initialization", "provisioned")
		if err != nil {
			return false, fmt.Errorf("readProvisioned: reading status.initialization.provisioned: %w", err)
		}

		return v, nil

	default:
		return false, fmt.Errorf("readProvisioned: %q: %w", contract, ErrUnknownContract)
	}
}
