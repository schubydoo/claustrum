//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreeCreateRegisteredLeaf pins the refusal of a create at a gone leaf
// that two unlocked entries name (be1c1ca5, measured on Linux). The
// stale step keeps both, and the text names the first entry by name (not measured). Both stay.
func TestWorktreeCreateRegisteredLeaf(t *testing.T) {
	f := newRmFixture(t)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w")
	for _, name := range []string{"x1", "x2"} {
		gone := filepath.Join(filepath.Dir(f.repo), "gone", name)
		runGit(t, f.repo, "worktree", "add", "-q", "-b", name, gone)
		writeFile(t, filepath.Join(f.repo, ".git", "worktrees", name, "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
	}
	if err := os.RemoveAll(filepath.Join(filepath.Dir(f.repo), "gone")); err != nil {
		t.Fatal(err)
	}
	raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": f.repo, "branchName": "b", "worktreePath": leaf}))
	want := "cannot add a worktree: `" + leaf + "` is still registered in this repository as a worktree " +
		"(entry \"x1\"); clear the stale registration first (git worktree prune), then try again"
	if text, code := createErrorField(t, raw); text != want || code != "worktree_add_failed" {
		t.Errorf("reply = %s\nwant error %q with worktree_add_failed", raw, want)
	}
	for _, name := range []string{"x1", "x2"} {
		mustExist(t, filepath.Join(f.repo, ".git", "worktrees", name, "gitdir"))
	}
}

// TestGenericHintChars pins the characters that give the unlock hint with no path
// on Linux and macOS: `$` (measured on Linux and macOS), `'` and a backslash
// (measured on Linux).
func TestGenericHintChars(t *testing.T) {
	for _, c := range []string{"$", "'", `\`} {
		if !strings.Contains(genericHintChars, c) {
			t.Errorf("genericHintChars = %q, want it to hold %q", genericHintChars, c)
		}
	}
}

// TestWorktreeCreateRegisteredLeafDaemonCommonDir pins that a daemon
// GIT_COMMON_DIR turns the registered-leaf test off. git then refuses the leaf
// with its own text, after the add (claustrum's own, not measured).
func TestWorktreeCreateRegisteredLeafDaemonCommonDir(t *testing.T) {
	f := newRmFixture(t)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w")
	runGit(t, f.repo, "worktree", "add", "-q", "-b", "old", leaf)
	runGit(t, f.repo, "worktree", "lock", leaf)
	if err := os.RemoveAll(leaf); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_COMMON_DIR", filepath.Join(f.repo, ".git"))
	raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": f.repo, "branchName": "b", "worktreePath": leaf}))
	text, code := createErrorField(t, raw)
	if !strings.HasPrefix(text, "git worktree add failed: ") || !strings.Contains(text, "missing but locked worktree") ||
		code != "worktree_add_failed" {
		t.Errorf("reply = %s\nwant git's own refusal of the locked leaf", raw)
	}
}
