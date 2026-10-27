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

// Command package-helm stages a chart with image defaults for local packaging.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/distribution/reference"
	"sigs.k8s.io/yaml"

	_ "crypto/sha256" // Register the digest algorithm accepted in image references.
)

var errInvalidPackagingInput = errors.New("invalid Helm packaging input")

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: package-helm SOURCE_CHART STAGED_CHART IMAGE")
		os.Exit(2)
	}
	if err := stageChart(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func stageChart(source, destination, image string) error {
	defaults, err := imageDefaults(image)
	if err != nil {
		return err
	}
	valuesPath := filepath.Join(source, "values.yaml")
	info, err := os.Lstat(valuesPath)
	if err != nil {
		return fmt.Errorf("checking chart values: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("chart values must be a regular file: %w", errInvalidPackagingInput)
	}
	data, err := os.ReadFile(valuesPath)
	if err != nil {
		return fmt.Errorf("reading chart values: %w", err)
	}
	data, err = setImageDefaults(data, defaults)
	if err != nil {
		return err
	}
	// CopyFS refuses to overwrite existing files and does not follow file symlinks.
	if err := os.CopyFS(destination, os.DirFS(source)); err != nil {
		return fmt.Errorf("staging chart: %w", err)
	}
	if err := os.WriteFile(filepath.Join(destination, "values.yaml"), data, 0o600); err != nil {
		return fmt.Errorf("writing staged chart values: %w", err)
	}
	return nil
}

func imageDefaults(image string) (map[string]string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return nil, fmt.Errorf("parsing IMAGE: %w", err)
	}
	defaults := map[string]string{"repository": named.Name(), "tag": "", "digest": ""}
	switch selected := named.(type) {
	case reference.Canonical:
		defaults["digest"] = selected.Digest().String()
	case reference.Tagged:
		defaults["tag"] = selected.Tag()
	default:
		return nil, fmt.Errorf("IMAGE requires an explicit tag or digest: %w", errInvalidPackagingInput)
	}
	return defaults, nil
}

func setImageDefaults(data []byte, defaults map[string]string) ([]byte, error) {
	var values map[string]any
	// Retain exact integer payloads rather than decoding them through float64.
	if err := yaml.UnmarshalStrict(data, &values, func(decoder *json.Decoder) *json.Decoder {
		decoder.UseNumber()
		return decoder
	}); err != nil {
		return nil, fmt.Errorf("decoding chart values: %w", err)
	}
	image, ok := values["image"].(map[string]any)
	if !ok || image == nil {
		return nil, fmt.Errorf("chart values need an image object: %w", errInvalidPackagingInput)
	}
	for key, value := range defaults {
		image[key] = value
	}
	data, err := yaml.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encoding staged chart values: %w", err)
	}
	return data, nil
}
