package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A worktree whose session folder was deleted out from under git leaves a stale
// "prunable" registration. 7d193f89 drops that record on a re-create at the same
// path and succeeds; claustrum used to fail "missing but already registered". Only
// the target's registration is dropped — a second stale registration is left in
// place. Measured against 7d193f89.
func TestWorktreeCreateDropsStaleRegistration(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	s := newTestServer(t)

	mk := func(branch, name string) string {
		wp := filepath.Join(repo, ".claude", "worktrees", name)
		return dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": repo, "branchName": branch, "worktreePath": wp}))
	}

	// Create two worktrees, then delete both directories, leaving two stale records.
	for _, w := range []struct{ branch, name string }{{"a", "wa"}, {"b", "wb"}} {
		if raw := mk(w.branch, w.name); !strings.Contains(raw, `"success":true`) {
			t.Fatalf("create %s = %s, want success", w.name, raw)
		}
	}
	if err := os.RemoveAll(filepath.Join(repo, ".claude", "worktrees", "wa")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(repo, ".claude", "worktrees", "wb")); err != nil {
		t.Fatal(err)
	}

	// Re-create wa at its old path: the stale record is dropped and it succeeds.
	if raw := mk("a2", "wa"); !strings.Contains(raw, `"success":true`) {
		t.Errorf("re-create over a stale registration = %s, want success", raw)
	}
	// wb's stale registration is untouched — only the target's was dropped.
	if _, err := os.Stat(filepath.Join(repo, ".git", "worktrees", "wb")); err != nil {
		t.Errorf("wb registration was pruned too (%v); only the target's should be dropped", err)
	}
}

// dropStaleWorktreeRegistration is a no-op on a repo that has no worktree
// registrations at all (no .git/worktrees) — the ReadDir simply fails and returns.
func TestDropStaleWorktreeRegistrationNoWorktrees(t *testing.T) {
	repo := t.TempDir()                                                  // not even a git repo — .git/worktrees absent
	dropStaleWorktreeRegistration(repo, filepath.Join(repo, "whatever")) // must not panic
}
