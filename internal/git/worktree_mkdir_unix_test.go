//go:build unix

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 7d193f89 creates the worktree directory itself before `git worktree add`, so
// an unwritable parent fails as
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

	h := newTestGitDeps()
	raw := gitDispatch(t, h, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "x", "worktreePath": filepath.Join(holder, "wt")}))
	if !strings.Contains(raw, `"errorCode":"mkdir_failed"`) ||
		!strings.Contains(raw, "failed to create worktree directory: mkdirat wt:") {
		t.Errorf("create with unwritable parent = %s, want mkdir_failed with 'mkdirat wt:'", raw)
	}
}

// rmdirWorktreeLeaf removes the leaf by its name through a handle of the parent
// that it opened. The hook moves that parent away and puts a new parent with an
// empty leaf at the old path. The leaf inside the opened parent goes, and the new
// one stays. A removal by the full path takes the new leaf instead. A file at the
// leaf stays. All paths are in a t.TempDir().
func TestRmdirWorktreeLeafRemovesThroughTheParentHandle(t *testing.T) {
	t.Run("the_opened_parent", func(t *testing.T) {
		dir := t.TempDir()
		parent := filepath.Join(dir, "p")
		moved := filepath.Join(dir, "moved")
		leaf := filepath.Join(parent, "w1")
		if err := os.MkdirAll(leaf, 0o755); err != nil {
			t.Fatal(err)
		}
		leafParentOpenedHook = func() {
			if err := os.Rename(parent, moved); err != nil {
				t.Error(err)
			}
			if err := os.MkdirAll(leaf, 0o755); err != nil {
				t.Error(err)
			}
		}
		t.Cleanup(func() { leafParentOpenedHook = nil })
		if err := rmdirWorktreeLeaf(leaf + "/"); err != nil {
			t.Fatalf("rmdirWorktreeLeaf: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(moved, "w1")); !os.IsNotExist(err) {
			t.Errorf("the leaf in the opened parent survived: Lstat err %v", err)
		}
		if _, err := os.Lstat(leaf); err != nil {
			t.Errorf("the leaf in the new parent went: %v", err)
		}
	})
	t.Run("keeps_a_file", func(t *testing.T) {
		leaf := filepath.Join(t.TempDir(), "w1")
		if err := os.WriteFile(leaf, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := rmdirWorktreeLeaf(leaf); err == nil {
			t.Error("rmdirWorktreeLeaf on a file: no error")
		}
		if _, err := os.Lstat(leaf); err != nil {
			t.Errorf("the file at the leaf went: %v", err)
		}
	})
}
