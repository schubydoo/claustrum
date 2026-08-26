//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 7d193f89 creates the worktree directory itself (openat parent + mkdirat leaf)
// before `git worktree add`, so an unwritable parent fails as
// "failed to create worktree directory: mkdirat <leaf>: permission denied" with
// errorCode mkdir_failed — NOT git's worktree_add_failed, and NOT os.Mkdir's
// "mkdir <full path>". The `mkdirat <leaf>` wording is the wire-visible bit this
// pins. Unix + non-root (relies on a 0500 parent to deny the leaf mkdir).
func TestWorktreeCreateMkdirLeafFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this case relies on")
	}
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	holder := filepath.Join(repo, ".claude", "worktrees")
	if err := os.MkdirAll(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(holder, 0o500); err != nil { // leaf mkdir denied
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(holder, 0o700) })

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "x", "worktreePath": filepath.Join(holder, "wt")}))
	if !strings.Contains(raw, `"errorCode":"mkdir_failed"`) ||
		!strings.Contains(raw, "failed to create worktree directory: mkdirat wt:") {
		t.Errorf("create with unwritable parent = %s, want mkdir_failed with 'mkdirat wt:'", raw)
	}
}
