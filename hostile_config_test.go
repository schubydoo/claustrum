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

// A nonexistent (or unreadable) path/baseRepo makes `git -C <dir> config` fail to
// chdir, which is NOT a hostile config: git could not reach a repo at all. The gate
// must fall through to the normal not-a-repo shape, matching 7d193f89 (measured: a
// nonexistent path answers isRepo:false / not_a_repo / success, not the -32603
// refusal). Before the fix, hostileConfigRefusal fired on the chdir failure and
// changed the frame on this fully client-controlled honest input.
func TestHostileConfigNotFiredOnMissingDir(t *testing.T) {
	requireGit(t)
	gone := filepath.Join(t.TempDir(), "does-not-exist")
	s := newTestServer(t)

	for _, method := range []string{"git.info", "git.status", "git.list_branches"} {
		raw := dispatchRaw(t, s, rpcLine(t, method, map[string]any{"path": gone, "baseRepo": gone}))
		if !strings.Contains(raw, `"isRepo":false`) || strings.Contains(raw, "config-defined hooks") {
			t.Errorf("%s on a missing dir = %s, want isRepo:false and no hostile refusal", method, raw)
		}
	}

	wc := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": gone, "branchName": "w", "worktreePath": filepath.Join(gone, ".claude", "worktrees", "w")}))
	if !strings.Contains(wc, `"errorCode":"not_a_repo"`) || strings.Contains(wc, "config-defined hooks") {
		t.Errorf("worktree_create on a missing dir = %s, want not_a_repo", wc)
	}

	wr := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": gone, "worktreePath": filepath.Join(gone, ".claude", "worktrees", "w")}))
	if !strings.Contains(wr, `"success":true`) {
		t.Errorf("worktree_remove on a missing dir = %s, want success:true", wr)
	}
}
