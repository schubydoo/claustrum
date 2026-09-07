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
