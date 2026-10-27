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

// Command verify-headers checks the repository's first-party Go license headers.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var errHeader = errors.New("license header does not match the Numspot Go policy")

const legacyHeader = "// Copyright 2026 Numspot.\n// SPDX-License-Identifier: Apache-2.0\n"

func main() {
	fix := flag.Bool("fix", false, "Expand legacy headers on authored Go files; never edit generated or foreign-attributed files")
	root := flag.String("root", ".", "Repository root")
	flag.Parse()
	if err := run(*root, *fix); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string, fix bool) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolving header root: %w", err)
	}
	header, err := loadHeader(root)
	if err != nil {
		return err
	}
	var failures []error
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("locating source: %w", err)
		}
		if entry.IsDir() {
			return skipHeaderDirectory(filepath.ToSlash(rel))
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		if err := checkHeader(path, entry, header, fix); err != nil {
			failures = append(failures, err)
		}
		return nil
	})
	if err != nil {
		failures = append(failures, fmt.Errorf("walking sources: %w", err))
	}
	return errors.Join(failures...)
}

func loadHeader(root string) ([]byte, error) {
	header, err := os.ReadFile(filepath.Join(root, "hack", "boilerplate.go.txt"))
	if err != nil {
		return nil, fmt.Errorf("reading boilerplate: %w", err)
	}
	header = append(bytes.TrimRight(header, "\n"), '\n')
	if !bytes.HasPrefix(header, []byte("// Copyright 2026 Numspot.\n")) ||
		!bytes.Contains(header, []byte("Licensed under the Apache License, Version 2.0")) {
		return nil, fmt.Errorf("invalid boilerplate template: %w", errHeader)
	}
	return header, nil
}

func skipHeaderDirectory(path string) error {
	switch filepath.Base(path) {
	case ".git", ".tools", ".idea", ".vscode", ".claude", ".opencode", ".kube", "vendor", "third_party":
		return filepath.SkipDir
	}
	if path == "out" || path == "bin" || path == "tests/crds" || path == "helm/charts" {
		return filepath.SkipDir
	}
	return nil
}

func checkHeader(path string, entry fs.DirEntry, header []byte, fix bool) error {
	if !entry.Type().IsRegular() {
		return fmt.Errorf("%s: non-regular source requires explicit review: %w", path, errHeader)
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	offset := headerOffset(source)
	if bytes.HasPrefix(source[offset:], header) {
		return nil
	}
	if !fix {
		return fmt.Errorf("%s: %w", path, errHeader)
	}
	if strings.HasPrefix(filepath.Base(path), "zz_generated") {
		return fmt.Errorf("%s: regenerate reserved output instead of rewriting it: %w", path, errHeader)
	}
	updated, err := expandHeader(source, offset, header)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	info, err := entry.Info()
	if err != nil {
		return fmt.Errorf("reading file mode: %w", err)
	}
	if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func expandHeader(source []byte, offset int, header []byte) ([]byte, error) {
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, "", source, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing Go preamble: %w", err)
	}
	if ast.IsGenerated(parsed) {
		return nil, fmt.Errorf("regenerate this file instead of rewriting it: %w", errHeader)
	}
	body := source[offset:]
	preamble := source[offset:files.Position(parsed.Package).Offset]
	if bytes.HasPrefix(body, []byte(legacyHeader)) {
		body = body[len(legacyHeader):]
		preamble = preamble[len(legacyHeader):]
	}
	if hasAttribution(preamble) {
		return nil, fmt.Errorf("existing attribution requires manual review: %w", errHeader)
	}
	result := append([]byte{}, source[:offset]...)
	result = append(result, header...)
	result = append(result, '\n')
	return append(result, bytes.TrimLeft(body, "\n")...), nil
}

func hasAttribution(preamble []byte) bool {
	lower := strings.ToLower(string(preamble))
	return strings.Contains(lower, "copyright") || strings.Contains(lower, "spdx-license-identifier:") ||
		strings.Contains(lower, "licensed under")
}

func headerOffset(source []byte) int {
	offset := 0
	for offset < len(source) {
		line, _, found := bytes.Cut(source[offset:], []byte("\n"))
		if !found {
			break
		}
		if len(line) != 0 && !bytes.HasPrefix(line, []byte("//go:build ")) && !bytes.HasPrefix(line, []byte("// +build ")) {
			break
		}
		offset += len(line) + 1
	}
	return offset
}
