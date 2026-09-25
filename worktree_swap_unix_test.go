//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The post-add identity check (verifyCreatedWorktree) refuses a create whose leaf
// was swapped between mkdirWorktreeLeaf and `git worktree add`. No honest input can
// reach it, so the swap is staged by a `git` on PATH that delegates to the real git
// and then replaces the leaf with a directory of its own — the RPC counterpart to
// the seam-level TestVerifyCreatedWorktree.
//
// The replacement is created ALONGSIDE the original before the original is removed,
// so it cannot reuse the freed inode and os.SameFile is deterministic (the same
// hazard TestVerifyCreatedWorktree records). Unix-only: it needs a shell stub and an
// executable bit.
func TestWorktreeCreateRefusesSwappedLeaf(t *testing.T) {
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(repo, ".claude", "worktrees", "wt")
	swap := filepath.Join(repo, ".claude", "worktrees", "swap")

	// Only `worktree add` is intercepted; every other call (the config precursor,
	// rev-parse, read-tree) goes straight through, so the create reaches the check.
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \" $* \" in\n" +
		"  *\" worktree add \"*)\n" +
		"    \"" + realGit + "\" \"$@\" || exit $?\n" +
		"    mkdir \"" + swap + "\" && rm -rf \"" + wt + "\" && mv \"" + swap + "\" \"" + wt + "\"\n" +
		"    exit 0 ;;\n" +
		"esac\n" +
		"exec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": wt}))
	if !strings.Contains(raw, "was not populated by git worktree add") ||
		!strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
		t.Errorf("create over a swapped leaf = %s, want the not-populated refusal", raw)
	}
	if strings.Contains(raw, `"success":true`) {
		t.Errorf("create over a swapped leaf = %s, want success:false", raw)
	}
}

// TestCheckpointHoldsLeafAndParent pins that, on unix, the create's checkpoint
// holds the leaf and its parent open until release. The references hold open
// descriptors on both during the create (measured in /proc/<pid>/fd on a Linux
// VM). The held leaf keeps its inode allocated, so a directory made at the same
// path after a delete gets a new inode number, and verifyCreatedWorktree reports
// it. Without the hold, ext4 reused the number 4 times in 6 on a Linux VM, and the
// replacement passed the check. A tmpfs did not reuse the number in local runs, so
// this test checks the hold itself, then the refusal after many replacements.
func TestCheckpointHoldsLeafAndParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "worktrees")
	leaf := filepath.Join(parent, "w1")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	cp := checkpointCreatedWorktree(leaf + "/")
	if cp.info == nil {
		t.Fatal("checkpoint did not capture the leaf")
	}
	if len(cp.held) != 2 {
		cp.release()
		t.Fatalf("checkpoint holds %d handles, want 2 (the leaf and its parent)", len(cp.held))
	}
	for i, want := range []string{leaf, parent} {
		got, err := cp.held[i].Stat()
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(want)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(got, fi) {
			t.Errorf("held handle %d is not %s", i, want)
		}
	}
	for i := range 200 {
		if err := os.RemoveAll(leaf); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(leaf, 0o755); err != nil {
			t.Fatal(err)
		}
		if msg := verifyCreatedWorktree(leaf, cp); !strings.Contains(msg, "was not populated by git worktree add") {
			t.Fatalf("replacement %d: verify = %q, want the not-populated refusal", i, msg)
		}
	}
	held := cp.held[0]
	cp.release()
	if _, err := held.Stat(); err == nil {
		t.Errorf("the leaf handle is still open after release")
	}
}
