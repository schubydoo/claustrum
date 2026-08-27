package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A worktree whose `.git` pointer is forged to name a SIBLING worktree's admin dir
// (still under `<repo>/.git/worktrees`, so the containment check alone passes) must
// NOT have that sibling's registration pruned when it is removed. 7d193f89 leaves the
// sibling registered (measured on an ephemeral VM: sibling registration survives,
// reply success:true); before the back-pointer check claustrum pruned it. The prune
// is off-wire, so the reply is success:true either way — the divergence is the
// filesystem side effect on an unrelated worktree.
func TestWorktreeRemoveForgedSiblingPointerNotPruned(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wtA := filepath.Join(repo, ".claude", "worktrees", "A")
	wtB := filepath.Join(repo, ".claude", "worktrees", "B")
	runGit(t, repo, "worktree", "add", "-q", wtA, "-b", "A")
	runGit(t, repo, "worktree", "add", "-q", wtB, "-b", "B")

	// Forge A's `.git` to point at B's admin directory.
	adminB := filepath.Join(repo, ".git", "worktrees", "B")
	if err := os.WriteFile(filepath.Join(wtA, ".git"), []byte("gitdir: "+adminB+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wtA}))
	if !strings.Contains(raw, `"success":true`) {
		t.Errorf("worktree_remove of forged A = %s, want success:true", raw)
	}
	if _, err := os.Stat(adminB); err != nil {
		t.Errorf("removing forged A pruned sibling B's registration (%v); 7d193f89 leaves it in place", err)
	}
}
