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

// Command check-dco verifies author-matching sign-offs in a Git commit range.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var (
	errInvalidRange   = errors.New("base and head must be full commit IDs")
	errNoCommits      = errors.New("no contribution commits to check")
	errShallowHistory = errors.New("complete Git history is required; fetch with --unshallow")
	errUnsignedCommit = errors.New("missing author sign-off")
)

const diagnosticCommitLimit = 10

func main() {
	base := flag.String("base", "", "full base commit ID to exclude")
	head := flag.String("head", "", "full contribution head commit ID to include")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "check-dco: unexpected positional arguments")
		os.Exit(1)
	}
	count, err := checkCommits(context.Background(), ".", *base, *head)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-dco:", err)
		os.Exit(1)
	}
	fmt.Printf("DCO: verified author-matching sign-offs on %d commits\n", count)
}

func checkCommits(ctx context.Context, directory, base, head string) (int, error) {
	if !validCommitID(base) || !validCommitID(head) {
		return 0, errInvalidRange
	}
	shallow, err := gitOutput(ctx, directory, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(shallow) != "false" {
		return 0, errShallowHistory
	}
	history, err := gitOutput(ctx, directory, "rev-list", "--reverse", base+".."+head, "--")
	if err != nil {
		return 0, err
	}
	commits := strings.Fields(history)
	if len(commits) == 0 {
		return 0, errNoCommits
	}
	invalid, ids, err := unsignedCommits(ctx, directory, commits)
	if err != nil {
		return 0, err
	}
	if invalid > 0 {
		// Do not publish commit messages or author-controlled content as workflow commands.
		return len(commits), fmt.Errorf("%d commits lack an author-matching Signed-off-by trailer (first %d IDs: %s): %w",
			invalid, len(ids), strings.Join(ids, ", "), errUnsignedCommit)
	}
	return len(commits), nil
}

func unsignedCommits(ctx context.Context, directory string, commits []string) (invalid int, ids []string, err error) {
	ids = make([]string, 0, diagnosticCommitLimit)
	for _, commit := range commits {
		// Native Git parses the trailer block, not arbitrary sign-off text in the body.
		metadata, err := gitOutput(ctx, directory, "show", "--no-patch", "--no-show-signature",
			"--format=%an%x00%ae%x00%(trailers:key=Signed-off-by,valueonly,unfold)", commit, "--")
		if err != nil {
			return 0, nil, err
		}
		if authorSignedOff(metadata) {
			continue
		}
		invalid++
		if len(ids) < diagnosticCommitLimit {
			ids = append(ids, commit[:12])
		}
	}
	return invalid, ids, nil
}

func validCommitID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func authorSignedOff(metadata string) bool {
	fields := strings.SplitN(metadata, "\x00", 3)
	if len(fields) != 3 || strings.TrimSpace(fields[0]) == "" || strings.TrimSpace(fields[1]) == "" {
		return false
	}
	expected := fields[0] + " <" + fields[1] + ">"
	for trailer := range strings.SplitSeq(fields[2], "\n") {
		if strings.TrimSpace(trailer) == expected {
			return true
		}
	}
	return false
}

func gitOutput(ctx context.Context, directory string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects"}, args...)...)
	cmd.Dir = directory
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return string(output), nil
}
