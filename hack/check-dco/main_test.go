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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const authorSignOff = "Signed-off-by: Test Author <author@example.com>"

func TestDCOAuthorMatchingTrailers(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		wantError     bool
	}{
		{name: "signed", message: "feat: example\n\n" + authorSignOff},
		{name: "missing", message: "feat: example", wantError: true},
		{name: "wrong-name", message: "feat: example\n\nSigned-off-by: Someone Else <author@example.com>", wantError: true},
		{name: "wrong-email", message: "feat: example\n\nSigned-off-by: Test Author <other@example.com>", wantError: true},
		{name: "additional-signer", message: "feat: example\n\nSigned-off-by: Someone Else <other@example.com>\n" + authorSignOff},
		{name: "case-insensitive-key", message: "feat: example\n\nsigned-OFF-by: Test Author <author@example.com>"},
		{name: "not-a-footer", message: "feat: example\n\n" + authorSignOff + "\n\nThis is only a body example.", wantError: true},
		{name: "subject-only", message: authorSignOff, wantError: true},
		{name: "malformed", message: "feat: example\n\nSigned-off-by: Test Author", wantError: true},
		{name: "untrusted-body", message: "feat: ::error::SECRET-CANARY\n\n" + strings.Repeat("SECRET-CANARY", 200), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := newDCOHistory(t)
			base := history.commit(t, "unsigned history is outside this PR")
			head := history.commit(t, tc.message, base)
			count, err := checkCommits(t.Context(), history.dir, base, head)
			assert.Equal(t, 1, count)
			if tc.wantError {
				require.ErrorIs(t, err, errUnsignedCommit)
				assert.ErrorContains(t, err, head[:12])
				assert.NotContains(t, err.Error(), "SECRET-CANARY")
				assert.NotContains(t, err.Error(), "::error::")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDCOChecksEarlierCommitsAndRealMergeCommits(t *testing.T) {
	t.Run("earlier-unsigned", func(t *testing.T) {
		history := newDCOHistory(t)
		base := history.commit(t, "base")
		unsigned := history.commit(t, "unsigned contribution", base)
		head := history.commit(t, "signed tip\n\n"+authorSignOff, unsigned)
		count, err := checkCommits(t.Context(), history.dir, base, head)
		require.ErrorIs(t, err, errUnsignedCommit)
		assert.Equal(t, 2, count)
		assert.ErrorContains(t, err, unsigned[:12])
	})
	for _, signed := range []bool{false, true} {
		t.Run(fmt.Sprintf("real-merge-signed=%t", signed), func(t *testing.T) {
			history := newDCOHistory(t)
			base := history.commit(t, "base")
			left := history.commit(t, "left\n\n"+authorSignOff, base)
			right := history.commit(t, "right\n\n"+authorSignOff, base)
			message := "merge contributions"
			if signed {
				message += "\n\n" + authorSignOff
			}
			head := history.commit(t, message, left, right)
			count, err := checkCommits(t.Context(), history.dir, base, head)
			assert.Equal(t, 3, count)
			if signed {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errUnsignedCommit)
				assert.ErrorContains(t, err, head[:12])
			}
		})
	}
}

func TestDCORangeUsesActualHeadAndExcludesBaseHistory(t *testing.T) {
	history := newDCOHistory(t)
	common := history.commit(t, "common unsigned history")
	base := history.commit(t, "unsigned base-branch advancement", common)
	head := history.commit(t, "signed contribution\n\n"+authorSignOff, common)
	synthetic := history.commit(t, "GitHub synthetic merge without sign-off", base, head)
	count, err := checkCommits(t.Context(), history.dir, base, head)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	_, err = checkCommits(t.Context(), history.dir, base, synthetic)
	require.ErrorIs(t, err, errUnsignedCommit, "the workflow must pass the PR head, not the synthetic merge SHA")
}

func TestDCOChecksBeyondAPIPaginationLimits(t *testing.T) {
	history := newDCOHistory(t)
	base := history.commit(t, "base")
	head := base
	for i := range 260 {
		head = history.commit(t, fmt.Sprintf("signed contribution %d\n\n%s", i, authorSignOff), head)
	}
	head = history.commit(t, "late unsigned contribution", head)
	count, err := checkCommits(t.Context(), history.dir, base, head)
	require.ErrorIs(t, err, errUnsignedCommit)
	assert.Equal(t, 261, count)
	assert.ErrorContains(t, err, head[:12])
}

func TestDCORejectsInvalidOrUnreadableRanges(t *testing.T) {
	history := newDCOHistory(t)
	base := history.commit(t, "base")
	head := history.commit(t, "signed\n\n"+authorSignOff, base)
	for _, invalid := range []string{"", "HEAD", "--all", strings.Repeat("g", 40)} {
		_, err := checkCommits(t.Context(), history.dir, invalid, head)
		require.ErrorIs(t, err, errInvalidRange)
		_, err = checkCommits(t.Context(), history.dir, base, invalid)
		require.ErrorIs(t, err, errInvalidRange)
	}
	_, err := checkCommits(t.Context(), history.dir, base, base)
	require.ErrorIs(t, err, errNoCommits)
	_, err = checkCommits(t.Context(), history.dir, strings.Repeat("f", 40), head)
	var exitError *exec.ExitError
	require.ErrorAs(t, err, &exitError, "Git failure must not become an empty passing range")
}

func TestDCORejectsShallowHistory(t *testing.T) {
	history := newDCOHistory(t)
	base := history.commit(t, "base")
	unsigned := history.commit(t, "unsigned contribution", base)
	head := history.commit(t, "signed tip\n\n"+authorSignOff, unsigned)
	// A shallow boundary can hide the unsigned parent even when the base object exists.
	require.NoError(t, os.WriteFile(filepath.Join(history.dir, ".git", "shallow"), []byte(head+"\n"), 0o600))
	_, err := checkCommits(t.Context(), history.dir, base, head)
	require.ErrorIs(t, err, errShallowHistory, "incomplete history must not produce a passing partial check")
}

func TestDCOBoundsFailureDiagnostics(t *testing.T) {
	history := newDCOHistory(t)
	base := history.commit(t, "base")
	head := base
	for range 12 {
		head = history.commit(t, "unsigned SECRET-CANARY", head)
	}
	count, err := checkCommits(t.Context(), history.dir, base, head)
	require.ErrorIs(t, err, errUnsignedCommit)
	assert.Equal(t, 12, count)
	assert.ErrorContains(t, err, "12 commits")
	assert.Less(t, len(err.Error()), 1024)
	assert.NotContains(t, err.Error(), "SECRET-CANARY")
}

type dcoHistory struct {
	dir, tree string
}

func newDCOHistory(t *testing.T) dcoHistory {
	t.Helper()
	history := dcoHistory{dir: t.TempDir()}
	history.git(t, "", "init", "--quiet", "--initial-branch=main")
	history.tree = history.git(t, "", "mktree")
	return history
}

func (h dcoHistory) commit(t *testing.T, message string, parents ...string) string {
	t.Helper()
	args := make([]string, 0, 4+2*len(parents))
	args = append(args, "commit-tree", h.tree, "-F", "-")
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	return h.git(t, message, args...)
}

func (h dcoHistory) git(t *testing.T, input string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = h.dir
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + h.dir,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_AUTHOR_NAME=Test Author", "GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Test Author", "GIT_COMMITTER_EMAIL=author@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", args[0], output)
	return strings.TrimSpace(string(output))
}
