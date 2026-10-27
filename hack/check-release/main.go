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

// Command check-release validates candidate versions, images and metadata together.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/blang/semver/v4"
	"github.com/distribution/reference"
	"sigs.k8s.io/yaml"

	_ "crypto/sha256" // Register the digest algorithm accepted in image references.
)

var errInvalidRelease = errors.New("invalid release inputs")

type providerMetadata struct {
	APIVersion    string `json:"apiVersion"`
	Kind          string `json:"kind"`
	ReleaseSeries []struct {
		Major    uint64 `json:"major"`
		Minor    uint64 `json:"minor"`
		Contract string `json:"contract"`
	} `json:"releaseSeries"`
}

func main() {
	if err := run(os.Getenv("VERSION"), os.Getenv("IMAGE")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(version, image string) error {
	if !strings.HasPrefix(version, "v") {
		return fmt.Errorf("VERSION needs a v prefix: %w", errInvalidRelease)
	}
	v, err := semver.Parse(strings.TrimPrefix(version, "v"))
	if err != nil {
		return fmt.Errorf("parsing VERSION: %w", err)
	}
	if err := validateImage(image); err != nil {
		return err
	}
	data, err := os.ReadFile("metadata.yaml")
	if err != nil {
		return fmt.Errorf("reading provider metadata: %w", err)
	}
	var metadata providerMetadata
	if err := yaml.UnmarshalStrict(data, &metadata); err != nil {
		return fmt.Errorf("decoding provider metadata: %w", err)
	}
	if metadataSupportsVersion(metadata, v) {
		return nil
	}
	return fmt.Errorf("VERSION has no v1beta2 release series in metadata.yaml: %w", errInvalidRelease)
}

func metadataSupportsVersion(metadata providerMetadata, version semver.Version) bool {
	if metadata.Kind != "Metadata" || metadata.APIVersion != "clusterctl.cluster.x-k8s.io/v1alpha3" {
		return false
	}
	for _, series := range metadata.ReleaseSeries {
		if series.Major == version.Major && series.Minor == version.Minor && series.Contract == "v1beta2" {
			return true
		}
	}
	return false
}

func validateImage(image string) error {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return fmt.Errorf("parsing IMAGE: %w", err)
	}
	_, tagged := named.(reference.Tagged)
	_, digested := named.(reference.Canonical)
	if !tagged && !digested {
		return fmt.Errorf("IMAGE requires an explicit tag or digest: %w", errInvalidRelease)
	}
	return nil
}
