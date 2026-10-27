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

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratedVerification_RejectsExcludedGeneratedEntries(t *testing.T) {
	for _, name := range []string{"helm/crds/mgmt.yaml", "api/config/crd/bases/.env"} {
		root := t.TempDir()
		require.NoError(t, generateFixture(root))
		writeFixture(t, root, name, "must not be read or copied\n")
		err := verify(root, func(string) error {
			t.Fatal("generation must not run after an excluded generated entry")
			return nil
		})
		require.ErrorIs(t, err, errStaleGenerated)
	}
}

func TestGeneratedVerification_RejectsNestedTemporaryDirectories(t *testing.T) {
	for _, mode := range []string{"relative", "symlink", "case-alias"} {
		t.Run(mode, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("Unix TMPDIR and symlink behavior")
			}
			parent := t.TempDir()
			root := filepath.Join(parent, "source")
			require.NoError(t, generateFixture(root))
			t.Chdir(root)
			tmp := "."
			if mode == "symlink" {
				tmp = filepath.Join(parent, "alias")
				require.NoError(t, os.Symlink(root, tmp))
			}
			if mode == "case-alias" {
				tmp = filepath.Join(parent, "SOURCE")
				aliasInfo, err := os.Stat(tmp)
				if os.IsNotExist(err) {
					t.Skip("case-sensitive filesystem")
				}
				require.NoError(t, err)
				rootInfo, err := os.Stat(root)
				require.NoError(t, err)
				require.True(t, os.SameFile(rootInfo, aliasInfo))
			}
			t.Setenv("TMPDIR", tmp)
			before := fixtureSnapshot(t, root)
			err := verify(root, func(string) error {
				t.Fatal("generation must not run with nested scratch storage")
				return nil
			})
			require.ErrorIs(t, err, errUnsafeSource)
			assert.Equal(t, before, fixtureSnapshot(t, root))
		})
	}
}

func TestGeneratedVerification_PreservesAllSourceFiles(t *testing.T) {
	for _, fail := range []bool{false, true} {
		root := t.TempDir()
		require.NoError(t, generateFixture(root))
		writeFixture(t, root, "authored.go", "package example\n")
		writeFixture(t, root, "notes.txt", "authored input\n")
		writeFixture(t, root, ".env", "excluded sentinel\n")
		require.NoError(t, os.Chmod(filepath.Join(root, "authored.go"), 0o640))
		before := fixtureSnapshot(t, root)
		err := verify(root, func(scratch string) error {
			require.NoError(t, os.WriteFile(filepath.Join(scratch, "authored.go"), []byte("package changed\n"), 0o600))
			require.NoError(t, os.Remove(filepath.Join(scratch, "notes.txt")))
			if fail {
				return os.ErrPermission
			}
			return generateFixture(scratch)
		})
		if fail {
			require.ErrorIs(t, err, os.ErrPermission)
		} else {
			require.NoError(t, err)
		}
		assert.Equal(t, before, fixtureSnapshot(t, root))
	}
}

func fixtureSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		info, err := entry.Info()
		require.NoError(t, err)
		value := info.Mode().String()
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			value += string(data)
		}
		result[path] = value
		return nil
	})
	require.NoError(t, err)
	return result
}
