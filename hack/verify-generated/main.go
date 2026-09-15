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

// Command verify-generated compares the complete generated inventory with scratch generation.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	errStaleGenerated = errors.New("generated output is missing, stale or unexpected")
	errUnsafeSource   = errors.New("source snapshot contains an unsupported file or unsafe temporary directory")
)

func main() {
	if err := verify(".", generate); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(root string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "make", "gen")
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scratch generation: %w", err)
	}
	return nil
}

func verify(root string, generate func(string) error) error {
	root, err := canonicalPath(root)
	if err != nil {
		return fmt.Errorf("locating source root: %w", err)
	}
	scratch, err := scratchDirectory(root)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if err := copySource(root, scratch); err != nil {
		return err
	}
	expected, err := generatedInventory(root)
	if err != nil {
		return err
	}
	for path := range expected {
		if err := os.Remove(filepath.Join(scratch, path)); err != nil {
			return fmt.Errorf("clearing scratch output %s: %w", path, err)
		}
	}
	if err := generate(scratch); err != nil {
		return err
	}
	actual, err := generatedInventory(scratch)
	if err != nil {
		return err
	}
	return compareGenerated(expected, actual)
}

func scratchDirectory(root string) (directory string, err error) {
	created, err := os.MkdirTemp("", "composite-generated-")
	if err != nil {
		return "", fmt.Errorf("creating scratch directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(created)
		}
	}()
	directory, err = canonicalPath(created)
	if err != nil {
		return "", err
	}
	if err := requireOutsideSource(root, directory); err != nil {
		return "", err
	}
	return directory, nil
}

// Filesystem identity also detects case aliases on case-insensitive volumes.
func requireOutsideSource(root, directory string) error {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("checking source identity: %w", err)
	}
	for {
		info, err := os.Stat(directory)
		if err != nil {
			return fmt.Errorf("checking scratch ancestry: %w", err)
		}
		if os.SameFile(rootInfo, info) {
			return errUnsafeSource
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return nil
		}
		directory = parent
	}
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolving physical path: %w", err)
	}
	return resolved, nil
}

func copySource(root, target string) error {
	return walkSource(root, func(relative, path string, entry fs.DirEntry) error {
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return fmt.Errorf("creating snapshot directory: %w", err)
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading snapshot input: %w", err)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("reading snapshot permissions: %w", err)
		}
		if err := os.WriteFile(destination, data, info.Mode().Perm()); err != nil {
			return fmt.Errorf("writing snapshot input: %w", err)
		}
		return nil
	})
}

func generatedInventory(root string) (map[string][sha256.Size]byte, error) {
	result := map[string][sha256.Size]byte{}
	err := walkSource(root, func(relative, path string, entry fs.DirEntry) error {
		if entry.IsDir() {
			return nil
		}
		generated, err := generatedPath(relative, path)
		if err != nil {
			return err
		}
		if !generated {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading generated file: %w", err)
		}
		result[relative] = sha256.Sum256(data)
		return nil
	})
	return result, err
}

func generatedPath(relative, path string) (bool, error) {
	if generatedLocation(filepath.ToSlash(relative)) {
		return true, nil
	}
	if filepath.Ext(path) != ".go" {
		return false, nil
	}
	if strings.HasPrefix(filepath.Base(path), "zz_generated") {
		return true, nil
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return false, fmt.Errorf("reading Go generation marker: %w", err)
	}
	return ast.IsGenerated(parsed), nil
}

func generatedLocation(path string) bool {
	return strings.HasPrefix(path, "api/config/crd/bases/") || strings.HasPrefix(path, "helm/crds/") ||
		path == "config/rbac/role.yaml" || path == "helm/templates/_generated-rbac.tpl"
}

func compareGenerated(expected, actual map[string][sha256.Size]byte) error {
	if maps.Equal(expected, actual) {
		return nil
	}
	paths := map[string]bool{}
	for path, hash := range expected {
		if other, exists := actual[path]; !exists || hash != other {
			paths[path] = true
		}
	}
	for path := range actual {
		if _, exists := expected[path]; !exists {
			paths[path] = true
		}
	}
	return fmt.Errorf("%w:\n%s", errStaleGenerated, strings.Join(slices.Sorted(maps.Keys(paths)), "\n"))
}

func walkSource(root string, visit func(string, string, fs.DirEntry) error) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("locating snapshot input: %w", err)
		}
		if excluded(filepath.ToSlash(relative)) {
			if generatedLocation(filepath.ToSlash(relative)) {
				return fmt.Errorf("excluded entry in generated output %s: %w", relative, errStaleGenerated)
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("%s: %w", relative, errUnsafeSource)
		}
		return visit(relative, path, entry)
	})
	if err != nil {
		return fmt.Errorf("walking source snapshot: %w", err)
	}
	return nil
}

func excluded(path string) bool {
	for part := range strings.SplitSeq(path, "/") {
		switch part {
		case ".git", ".tools", ".idea", ".vscode", ".claude", ".opencode", ".kube", "vendor", "third_party":
			return true
		}
	}
	first, _, _ := strings.Cut(path, "/")
	if first == "out" || first == "bin" {
		return true
	}
	for _, directory := range []string{"helm/charts", "tests/crds", "tests/integration/test-results"} {
		if path == directory || strings.HasPrefix(path, directory+"/") {
			return true
		}
	}
	return excludedLocalFile(filepath.Base(path))
}

func excludedLocalFile(base string) bool {
	return base == "mgmt.yaml" || base == "kubeconfig" || strings.HasSuffix(base, ".kubeconfig") ||
		base == ".env" || strings.HasPrefix(base, ".env.") || base == "capi-composite-controller"
}
