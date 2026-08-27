package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// verifyCreatedWorktree is the post-`git worktree add` guard against a directory or
// ancestor swapped during the add. It is exercised at the function seam because no
// honest RPC input can reach a non-empty return (an unraced add preserves the leaf's
// identity). Each mutation below stands in for a distinct swap and maps to the
// reference's wording.
func TestVerifyCreatedWorktree(t *testing.T) {
	// An empty checkpoint (capture failed) must never invent a failure.
	if msg := verifyCreatedWorktree(filepath.Join(t.TempDir(), "gone"), worktreeCheckpoint{}); msg != "" {
		t.Errorf("empty checkpoint = %q, want no failure", msg)
	}

	base := t.TempDir()
	wt := filepath.Join(base, "d", "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	cp := checkpointCreatedWorktree(wt)
	if cp.info == nil {
		t.Fatal("checkpoint did not capture the leaf")
	}

	// Unchanged: the sound case an honest create always hits — passes.
	if msg := verifyCreatedWorktree(wt, cp); msg != "" {
		t.Errorf("sound worktree = %q, want pass", msg)
	}

	// The path no longer resolves (removed): "no longer leads to the directory".
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if msg := verifyCreatedWorktree(wt, cp); !strings.Contains(msg, "no longer leads to the directory that was created for the worktree") {
		t.Errorf("removed = %q, want the no-longer-leads refusal", msg)
	}

	// The path resolves but is no longer a directory (a file took its place):
	// "is no longer the directory that was created".
	if err := os.WriteFile(wt, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := verifyCreatedWorktree(wt, cp); !strings.Contains(msg, "is no longer the directory that was created for the worktree") {
		t.Errorf("file-at-path = %q, want the not-a-directory refusal", msg)
	}
	if err := os.Remove(wt); err != nil {
		t.Fatal(err)
	}

	// A fresh directory at the same path — resolves and is a dir, but a different
	// identity: "was not populated by git worktree add".
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if msg := verifyCreatedWorktree(wt, cp); !strings.Contains(msg, "was not populated by git worktree add") {
		t.Errorf("swapped dir = %q, want the not-populated refusal", msg)
	}
}
