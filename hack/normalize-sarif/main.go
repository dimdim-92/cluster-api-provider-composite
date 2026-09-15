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

// Command normalize-sarif rebases gosec's module-relative source locations for GitHub.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
)

var (
	errInvalidPrefix = errors.New("prefix must be a clean repository-relative directory")
	errInvalidReport = errors.New("invalid module-relative SARIF report")
)

func main() {
	prefix := flag.String("prefix", "", "Repository-relative directory containing the scanned module")
	flag.Parse()
	if err := normalize(os.Stdin, os.Stdout, *prefix); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func normalize(input io.Reader, output io.Writer, prefix string) error {
	if !validPrefix(prefix) {
		return errInvalidPrefix
	}
	decoder := json.NewDecoder(input)
	decoder.UseNumber()
	var report map[string]any
	if err := decoder.Decode(&report); err != nil {
		return fmt.Errorf("decoding SARIF: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errInvalidReport
	}
	if _, ok := report["runs"].([]any); !ok {
		return errInvalidReport
	}
	if err := rebaseLocations(report, prefix); err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		return fmt.Errorf("encoding SARIF: %w", err)
	}
	return nil
}

func validPrefix(prefix string) bool {
	return prefix != "" && prefix != "." && prefix != ".." && !path.IsAbs(prefix) &&
		path.Clean(prefix) == prefix && !strings.ContainsAny(prefix, ":\\") && !strings.HasPrefix(prefix, "../")
}

func rebaseLocations(value any, prefix string) error {
	switch value := value.(type) {
	case map[string]any:
		if location, ok := value["artifactLocation"].(map[string]any); ok {
			if err := rebaseURI(location, prefix); err != nil {
				return err
			}
		}
		for _, child := range value {
			if err := rebaseLocations(child, prefix); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := rebaseLocations(child, prefix); err != nil {
				return err
			}
		}
	}
	return nil
}

func rebaseURI(location map[string]any, prefix string) error {
	raw, present := location["uri"]
	if !present {
		return nil // Index-only artifact references have no source URI to rebase.
	}
	uri, ok := raw.(string)
	if !ok {
		return errInvalidReport
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return fmt.Errorf("parsing SARIF source URI: %w", err)
	}
	if uri == "" || parsed.IsAbs() || path.IsAbs(parsed.Path) {
		return nil
	}
	clean := path.Clean(parsed.Path)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return errInvalidReport
	}
	// Apply exactly once to a raw module report. A nested api/ directory inside
	// the API module is still a distinct path and must become api/api/....
	parsed.Path = path.Join(prefix, parsed.Path)
	parsed.RawPath = ""
	location["uri"] = parsed.String()
	return nil
}
