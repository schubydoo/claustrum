//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyClaudeDir gives up quietly when .claude cannot be read. A 0300 (exec, no
// read) directory makes ReadDir fail. Unix-only, non-root: root ignores the bits.
func TestCopyClaudeDirUnreadableSource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this case relies on")
	}
	dir := t.TempDir()
	claude := filepath.Join(dir, claudeDirName)
	if err := os.Mkdir(claude, 0o300); err != nil { // writable+exec, not readable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(claude, 0o700) })
	wt := filepath.Join(dir, "wt")
	if err := os.Mkdir(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	copyClaudeDir(dir, wt) // must not panic; nothing to copy
	if _, err := os.Stat(filepath.Join(wt, claudeDirName)); err == nil {
		t.Error("an unreadable .claude produced a destination directory")
	}
}

// copyDirRecursive recurses into a nested directory — the real oracle here, since
// breaking recursion drops the nested file — and skips a symlink within it. The
// symlink-skip assertion is belt-and-suspenders: copyFile's IsRegular guard already
// refuses the link (TestCopyFileSkipsNonRegular is the primary guard), so removing
// copyDirRecursive's explicit skip does not change this outcome; it stays for coverage
// of that branch. The tree is .claude/sub/deeper/{f.txt, link}, copied via copyClaudeDir.
func TestCopyClaudeDirRecursesAndSkipsNestedSymlink(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, claudeDirName, "sub", "deeper", "f.txt"), "x\n", 0o644)
	if err := os.Symlink("f.txt", filepath.Join(dir, claudeDirName, "sub", "deeper", "link")); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(dir, "wt")
	if err := os.Mkdir(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	copyClaudeDir(dir, wt)
	if _, err := os.Stat(filepath.Join(wt, claudeDirName, "sub", "deeper", "f.txt")); err != nil {
		t.Errorf("nested file was not copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(wt, claudeDirName, "sub", "deeper", "link")); err == nil {
		t.Error("a nested symlink was copied; copyDirRecursive must skip it")
	}
}

// git.worktree_remove's wipesHomeDir guard is defense-in-depth here: it fires only
// when a repo is an ancestor of home, so a worktreePath that passes the containment
// check still IS the home directory. With HOME under baseRepo, worktreePath=home
// reaches the guard and is refused before any delete. os.UserHomeDir reads $HOME on
// unix, so this is unix-scoped.
func TestWorktreeRemoveRefusesHomeUnderRepo(t *testing.T) {
	repo := t.TempDir()
	home := filepath.Join(repo, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": home}))
	if !strings.Contains(got, "must not be or contain the home directory") {
		t.Errorf("worktree_remove of home = %s, want the home-dir refusal", got)
	}
}
