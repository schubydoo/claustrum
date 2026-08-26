package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 7d193f89 refuses a baseRepo that sits inside a managed worktrees tree — beneath a
// `.claude/worktrees` directory, or beneath a directory holding a
// `.claude-managed-worktrees` marker — as an invalid trust root. It applies on
// git.list_branches (bare isRepo:false), git.worktree_create (errorCode
// "nested_base_repo"), and git.worktree_remove (no errorCode). Measured byte-for-byte
// against 7d193f89 on an ephemeral VM; git.info/git.status are NOT gated.
func TestBaseRepoUnderManagedWorktreesRefused(t *testing.T) {
	requireGit(t)
	const refusal = "baseRepo is inside a managed worktrees directory"

	// A repo nested under <base>/.claude/worktrees.
	base := t.TempDir()
	nested := filepath.Join(base, ".claude", "worktrees", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, nested, "init", "-q")
	runGit(t, nested, "commit", "-q", "--allow-empty", "-m", "init")

	// A repo beneath a directory holding the .claude-managed-worktrees marker.
	markBase := t.TempDir()
	writeFile(t, filepath.Join(markBase, managedWorktreesMarker), "", 0o644)
	markRepo := filepath.Join(markBase, "holder")
	if err := os.MkdirAll(markRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, markRepo, "init", "-q")
	runGit(t, markRepo, "commit", "-q", "--allow-empty", "-m", "init")

	s := newTestServer(t)
	for _, repo := range []string{nested, markRepo} {
		lb := dispatchRaw(t, s, rpcLine(t, "git.list_branches", map[string]any{"path": repo, "baseRepo": repo}))
		if !strings.Contains(lb, `"isRepo":false`) || strings.Contains(lb, `"master"`) {
			t.Errorf("list_branches(%s) = %s, want isRepo:false + empty branches", repo, lb)
		}
		wc := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": repo, "branchName": "x", "worktreePath": filepath.Join(repo, ".claude", "worktrees", "x")}))
		if !strings.Contains(wc, `"errorCode":"nested_base_repo"`) || !strings.Contains(wc, refusal) {
			t.Errorf("worktree_create(%s) = %s, want nested_base_repo refusal", repo, wc)
		}
		wr := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
			map[string]any{"baseRepo": repo, "worktreePath": filepath.Join(repo, ".claude", "worktrees", "x")}))
		if !strings.Contains(wr, `"success":false`) || !strings.Contains(wr, refusal) || strings.Contains(wr, `"errorCode"`) {
			t.Errorf("worktree_remove(%s) = %s, want the refusal with no errorCode", repo, wr)
		}
	}

	// Control: a normal top-level repo is NOT refused (list_branches lists its branch).
	normal := t.TempDir()
	runGit(t, normal, "init", "-q")
	runGit(t, normal, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, normal, "branch", "-M", "main")
	lb := dispatchRaw(t, s, rpcLine(t, "git.list_branches", map[string]any{"path": normal, "baseRepo": normal}))
	if !strings.Contains(lb, `"isRepo":true`) || !strings.Contains(lb, `"main"`) {
		t.Errorf("list_branches(normal) = %s, want isRepo:true with the branch", lb)
	}
}
