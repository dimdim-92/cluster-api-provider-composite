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

package profile

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
	"github.com/numspot/cluster-api-provider-composite/internal/mint"
)

// Keep this aligned with CompositeProviderSpec.EndpointPaths admission. Unlike
// policy pointers, endpoint paths are dotted ASCII properties rooted at spec.
var endpointPathPattern = regexp.MustCompile(`^spec\.[a-zA-Z0-9_]+(\.[a-zA-Z0-9_]+)*$`)

var (
	// ErrOverrideDenied means an effective consumer change is not explicitly permitted.
	ErrOverrideDenied = errors.New("profile override changes a field outside the allowed paths")
	// ErrProtectedOverride means an override changes profile- or controller-owned configuration.
	ErrProtectedOverride = errors.New("profile override changes protected configuration")
	// ErrInvalidPolicy means a pointer is malformed or traverses an unsupported array.
	ErrInvalidPolicy = errors.New("invalid profile override policy")
	// ErrInvalidCredentials means a Secret name or its native target paths are invalid.
	ErrInvalidCredentials = errors.New("invalid profile credential reference")
)

// Resolved is independent decoded configuration. It is not a persisted attachment
// or proof that the native API will accept the resulting payload.
type Resolved struct {
	InfraSpec map[string]any
	Bootstrap map[string]any
}

// Defaults contains source-template payloads, never embedded in the profile CR.
type Defaults struct {
	InfraSpec *apiextensionsv1.JSON
	Bootstrap *apiextensionsv1.JSON
}

// Configuration is the per-reconcile input to pure override resolution.
type Configuration struct {
	Defaults       *Defaults
	OverridePolicy *infrav1.CompositeProviderOverridePolicy
	EndpointPaths  []string
	CredentialsRef infrav1.ProviderCredentialsReference
}

// Resolve binds an optional credential reference into decoded profile defaults,
// composes explicit consumer input and validates effective changes while preserving
// exact JSON numbers. It does not inject endpoints or read Secret contents.
func Resolve(spec *Configuration, shared, infraOverride, bootstrapOverride *apiextensionsv1.JSON) (*Resolved, error) {
	if spec == nil {
		return nil, ErrInvalidProfile
	}
	inputs, err := decodeInputs(spec, shared, infraOverride, bootstrapOverride)
	if err != nil {
		return nil, err
	}
	policy, err := checkedOverridePolicy(spec.OverridePolicy)
	if err != nil {
		return nil, err
	}
	infra := mint.MergeInfrastructureObjects(inputs[0], inputs[3])
	bootstrapBaseline := mint.MergeBootstrapObjects(nil, inputs[1])
	// Shared input is itself a merge patch over an empty object; normalize it
	// before using it as a base so null members never leak into native payloads.
	sharedBootstrap := mint.MergeBootstrapObjects(nil, inputs[2])
	bootstrap := mint.MergeBootstrapObjects(mint.MergeBootstrapObjects(sharedBootstrap, inputs[1]), inputs[4])
	protected, err := protectedInfrastructurePaths(spec.EndpointPaths, policy.ProtectedInfraSpecPaths)
	if err != nil {
		return nil, err
	}
	for _, path := range spec.CredentialsRef.NamePaths {
		if !slices.Contains(protected, path) {
			protected = append(protected, path)
		}
	}
	if err := validateEffective(inputs[0], infra, policy.InfraSpecPaths, protected); err != nil {
		return nil, err
	}
	if err := validateEffective(bootstrapBaseline, bootstrap, policy.BootstrapPaths, policy.ProtectedBootstrapPaths); err != nil {
		return nil, err
	}
	return &Resolved{InfraSpec: infra, Bootstrap: bootstrap}, nil
}

func checkedOverridePolicy(policy *infrav1.CompositeProviderOverridePolicy) (*infrav1.CompositeProviderOverridePolicy, error) {
	if policy == nil {
		return &infrav1.CompositeProviderOverridePolicy{}, nil
	}
	for _, paths := range [][]infrav1.ProfileJSONPointer{policy.InfraSpecPaths, policy.BootstrapPaths, policy.ProtectedInfraSpecPaths, policy.ProtectedBootstrapPaths} {
		if len(paths) > 32 {
			return nil, ErrInvalidPolicy
		}
	}
	return policy, nil
}

func decodeInputs(spec *Configuration, shared, infraOverride, bootstrapOverride *apiextensionsv1.JSON) ([]map[string]any, error) {
	defaults := spec.Defaults
	if defaults == nil {
		defaults = &Defaults{}
	}
	inputs := []*apiextensionsv1.JSON{defaults.InfraSpec, defaults.Bootstrap, shared, infraOverride, bootstrapOverride}
	objects := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		var data []byte
		if input != nil {
			data = input.Raw
		}
		object, err := decodeObject(data)
		if err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := applyCredentials(objects[0], spec.CredentialsRef, spec.EndpointPaths); err != nil {
		return nil, err
	}
	return objects, nil
}

func protectedInfrastructurePaths(paths []string, declared []infrav1.ProfileJSONPointer) ([]infrav1.ProfileJSONPointer, error) {
	if len(paths) > 8 {
		return nil, ErrInvalidPolicy
	}
	protected := append([]infrav1.ProfileJSONPointer(nil), declared...)
	if len(paths) == 0 {
		paths = []string{"spec.controlPlaneEndpoint"}
	}
	for _, path := range paths {
		if len(path) > 512 || !endpointPathPattern.MatchString(path) {
			return nil, ErrInvalidPolicy
		}
		pointer := infrav1.ProfileJSONPointer("/" + strings.ReplaceAll(strings.TrimPrefix(path, "spec."), ".", "/"))
		if !slices.Contains(protected, pointer) {
			protected = append(protected, pointer)
		}
	}
	return protected, nil
}

func validateEffective(before, after map[string]any, allowed, protected []infrav1.ProfileJSONPointer) error {
	allow, err := compilePaths(allowed)
	if err != nil {
		return err
	}
	protect, err := compilePaths(protected)
	if err != nil {
		return err
	}
	if err := validateProtected(before, after, protect); err != nil {
		return err
	}
	for _, path := range allow {
		_, _, beforeArray := lookup(before, path)
		_, _, afterArray := lookup(after, path)
		if beforeArray || afterArray {
			return ErrInvalidPolicy
		}
	}
	return validateChanges(before, true, after, true, nil, allow)
}

func validateProtected(before, after map[string]any, protected [][]string) error {
	for _, path := range protected {
		old, oldFound, array := lookup(before, path)
		if array {
			return ErrInvalidPolicy
		}
		value, found, afterArray := lookup(after, path)
		if found != oldFound || !sameJSON(old, value) {
			return ErrProtectedOverride
		}
		if afterArray {
			return ErrInvalidPolicy
		}
	}
	return nil
}

func compilePaths(paths []infrav1.ProfileJSONPointer) ([][]string, error) {
	result := make([][]string, 0, len(paths))
	seen := map[infrav1.ProfileJSONPointer]bool{}
	for _, path := range paths {
		text := string(path)
		if !strings.HasPrefix(text, "/") || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 512 || seen[path] {
			return nil, ErrInvalidPolicy
		}
		seen[path] = true
		parts := strings.Split(text[1:], "/")
		for i, part := range parts {
			if !validPointerToken(part) {
				return nil, ErrInvalidPolicy
			}
			parts[i] = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		}
		result = append(result, parts)
	}
	return result, nil
}

func validPointerToken(token string) bool {
	if token == "" || strings.Contains(token, "*") {
		return false
	}
	for i := 0; i < len(token); i++ {
		if token[i] != '~' {
			continue
		}
		if i+1 >= len(token) || (token[i+1] != '0' && token[i+1] != '1') {
			return false
		}
		i++
	}
	return true
}

func lookup(object map[string]any, path []string) (value any, found, array bool) {
	value = object
	for _, segment := range path {
		if _, isArray := value.([]any); isArray {
			return nil, false, true
		}
		current, isObject := value.(map[string]any)
		if !isObject {
			return nil, false, false
		}
		value, found = current[segment]
		if !found {
			return nil, false, false
		}
	}
	return value, true, false
}

func validateChanges(before any, beforeFound bool, after any, afterFound bool, path []string, allowed [][]string) error {
	if beforeFound == afterFound && sameJSON(before, after) {
		return nil
	}
	if pathAllowed(path, allowed) {
		return nil
	}
	oldObject, oldMap := before.(map[string]any)
	newObject, newMap := after.(map[string]any)
	if (oldMap || !beforeFound) && (newMap || !afterFound) && len(oldObject)+len(newObject) > 0 {
		return validateObjectChanges(oldObject, newObject, path, allowed)
	}
	return ErrOverrideDenied
}

func validateObjectChanges(before, after map[string]any, path []string, allowed [][]string) error {
	keys := map[string]bool{}
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	for key := range keys {
		old, oldPresent := before[key]
		value, present := after[key]
		if err := validateChanges(old, oldPresent, value, present, append(path, key), allowed); err != nil {
			return err
		}
	}
	return nil
}

func pathAllowed(path []string, allowed [][]string) bool {
	for _, prefix := range allowed {
		if len(prefix) <= len(path) && slices.Equal(prefix, path[:len(prefix)]) {
			return true
		}
	}
	return false
}

func sameJSON(a, b any) bool {
	switch value := a.(type) {
	case json.Number:
		other, ok := b.(json.Number)
		if !ok {
			return false
		}
		first, err := canonicalNumber(value)
		if err != nil {
			return false
		}
		second, err := canonicalNumber(other)
		return err == nil && first == second
	case map[string]any:
		other, ok := b.(map[string]any)
		return ok && sameObject(value, other)
	case []any:
		other, ok := b.([]any)
		return ok && sameArray(value, other)
	default:
		return reflect.DeepEqual(a, b)
	}
}

func sameObject(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		other, exists := b[key]
		if !exists || !sameJSON(value, other) {
			return false
		}
	}
	return true
}

func sameArray(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameJSON(a[i], b[i]) {
			return false
		}
	}
	return true
}
