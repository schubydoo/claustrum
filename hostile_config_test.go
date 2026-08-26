package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 7d193f89 refuses every git method when the repo's config cannot be enumerated (so
// its config-defined hooks cannot be pinned off) — e.g. a corrupt .git/config. The
// frame is method-specific: -32603 for the read methods, a worktreeResult under
// errorCode worktree_add_failed for create, and a "could not check whether … is
// locked" message for remove. Measured byte-for-byte against 7d193f89 on a VM.
func TestHostileConfigRefusal(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(repo, ".claude", "worktrees", "w")
	runGit(t, repo, "worktree", "add", "-q", wt, "-b", "w")

	// Corrupt the main config so `git config --list` fails.
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n[bad section\nnotakey\n")
	_ = f.Close()

	s := newTestServer(t)
	const hostile = "config-defined hooks could not be pinned off; git not run: listing the configuration in force:"

	for _, m := range []struct {
		method string
		params map[string]any
	}{
		{"git.info", map[string]any{"path": repo, "baseRepo": repo}},
		{"git.status", map[string]any{"path": wt, "baseRepo": repo}},
		{"git.list_branches", map[string]any{"path": repo, "baseRepo": repo}},
	} {
		raw := dispatchRaw(t, s, rpcLine(t, m.method, m.params))
		if !strings.Contains(raw, `"code":-32603`) || !strings.Contains(raw, hostile) {
			t.Errorf("%s on corrupt config = %s, want -32603 hostile refusal", m.method, raw)
		}
	}

	wc := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "z", "worktreePath": filepath.Join(repo, ".claude", "worktrees", "z")}))
	if !strings.Contains(wc, `"errorCode":"worktree_add_failed"`) || !strings.Contains(wc, hostile) {
		t.Errorf("worktree_create on corrupt config = %s, want worktree_add_failed hostile", wc)
	}

	wr := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove", map[string]any{"baseRepo": repo, "worktreePath": wt}))
	if !strings.Contains(wr, "is locked (its registrations could not be examined); retry") {
		t.Errorf("worktree_remove on corrupt config = %s, want the registrations message", wr)
	}
}
