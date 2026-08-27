package main

import (
	"os"
	"path/filepath"
	"runtime"
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

	// Checkpointing a path that does not exist captures nothing (a stat failure) — a
	// best-effort guard, never a way to fail an honest create.
	if cp := checkpointCreatedWorktree(filepath.Join(t.TempDir(), "absent")); cp.info != nil {
		t.Errorf("checkpoint of a missing path captured %v, want empty", cp.info)
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

	// A directory with a DIFFERENT identity at the same path: "was not populated by git
	// worktree add". Two filesystem hazards make this sub-case OS-specific:
	//   - POSIX: a delete-then-recreate at the same path can reuse the freed inode, so
	//     os.SameFile reports the two identical (measured: flaky on the ubuntu CI runner).
	//     Creating the replacement ALONGSIDE the original (below) forces a distinct inode,
	//     making the check deterministic on POSIX rather than luck-of-the-allocator.
	//   - Windows: os.SameFile does NOT reliably distinguish two directories here at all —
	//     measured, the windows-latest runner reports the swapped directory as identical
	//     even when the two coexisted. So the identity check, and this sub-case, is
	//     POSIX-only; production verifyCreatedWorktree inherits that Windows limitation
	//     on its os.SameFile check.
	if runtime.GOOS == "windows" {
		return
	}
	orig := filepath.Join(base, "e", "wt")
	if err := os.MkdirAll(orig, 0o755); err != nil {
		t.Fatal(err)
	}
	origCP := checkpointCreatedWorktree(orig)
	if origCP.info == nil {
		t.Fatal("checkpoint did not capture the leaf")
	}
	swap := filepath.Join(base, "e", "swap")
	if err := os.MkdirAll(swap, 0o755); err != nil { // coexists with orig → distinct identity
		t.Fatal(err)
	}
	if err := os.RemoveAll(orig); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(swap, orig); err != nil { // orig's path now holds swap's identity
		t.Fatal(err)
	}
	if msg := verifyCreatedWorktree(orig, origCP); !strings.Contains(msg, "was not populated by git worktree add") {
		t.Errorf("swapped dir = %q, want the not-populated refusal", msg)
	}
}
