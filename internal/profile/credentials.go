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
	"errors"
	"slices"

	"k8s.io/apimachinery/pkg/util/validation"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

// applyCredentials writes only a Secret name into independently decoded native
// defaults. References are declarative: no Secret contents, provider-specific
// mapping, or source-template mutation belongs in this package.
func applyCredentials(defaults map[string]any, ref infrav1.ProviderCredentialsReference, endpointPaths []string) error {
	if ref.Name == "" && len(ref.NamePaths) == 0 {
		return nil
	}
	paths, err := credentialNamePaths(ref, endpointPaths)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := setCredentialName(defaults, path, ref.Name); err != nil {
			return err
		}
	}
	return nil
}

func credentialNamePaths(ref infrav1.ProviderCredentialsReference, endpointPaths []string) ([][]string, error) {
	if len(validation.IsDNS1123Subdomain(ref.Name)) != 0 || len(ref.NamePaths) == 0 || len(ref.NamePaths) > 8 {
		return nil, ErrInvalidCredentials
	}
	paths, err := compilePaths(ref.NamePaths)
	if err != nil {
		return nil, errors.Join(ErrInvalidCredentials, err)
	}
	endpoints, err := protectedInfrastructurePaths(endpointPaths, nil)
	if err != nil {
		return nil, errors.Join(ErrInvalidCredentials, err)
	}
	endpointTargets, err := compilePaths(endpoints)
	if err != nil {
		return nil, errors.Join(ErrInvalidCredentials, err)
	}
	if err := validateCredentialTargets(paths, endpointTargets); err != nil {
		return nil, err
	}
	return paths, nil
}

func validateCredentialTargets(paths, endpointTargets [][]string) error {
	for i, path := range paths {
		for _, endpoint := range endpointTargets {
			if pathsOverlap(path, endpoint) {
				return ErrInvalidCredentials
			}
		}
		for _, previous := range paths[:i] {
			if pathsOverlap(path, previous) {
				return ErrInvalidCredentials
			}
		}
	}
	return nil
}

func pathsOverlap(first, second []string) bool {
	length := min(len(first), len(second))
	return slices.Equal(first[:length], second[:length])
}

func setCredentialName(object map[string]any, path []string, name string) error {
	for _, segment := range path[:len(path)-1] {
		value, exists := object[segment]
		if !exists {
			next := map[string]any{}
			object[segment] = next
			object = next
			continue
		}
		next, ok := value.(map[string]any)
		if !ok {
			return ErrInvalidCredentials
		}
		object = next
	}
	leaf := path[len(path)-1]
	if value := object[leaf]; value != nil {
		if _, ok := value.(string); !ok {
			return ErrInvalidCredentials
		}
	}
	object[leaf] = name
	return nil
}
