package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRepoParentsWalkByHandles: on Linux the parent step makes each folder
// through the handle of the folder above it, as 87375cc7 does (measured on
// Linux). A `.claude` that is swapped for a new folder right after its open
// gets `worktrees` in the folder that was opened, and the new folder stays
// empty. Mutation: make `worktrees` by its full path, and it lands in the new
// folder.
func TestRepoParentsWalkByHandles(t *testing.T) {
	repo := t.TempDir()
	claude := filepath.Join(repo, ".claude")
	moved := filepath.Join(repo, ".claude-moved")
	mkdirForTest(t, claude)
	t.Cleanup(func() { parentWalkHook = nil })
	parentWalkHook = func(name string) {
		if name != ".claude" {
			return
		}
		if err := os.Rename(claude, moved); err != nil {
			t.Errorf("the rename of .claude: %v", err)
		}
		if err := os.Mkdir(claude, 0o755); err != nil {
			t.Errorf("the new .claude: %v", err)
		}
	}
	if err := mkdirRepoWorktreeParents(repo, filepath.Join(claude, "worktrees")); err != nil {
		t.Fatalf("mkdirRepoWorktreeParents = %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(moved, "worktrees")); err != nil || !fi.IsDir() {
		t.Errorf("worktrees in the opened folder: %v, want a folder", err)
	}
	if _, err := os.Lstat(filepath.Join(claude, "worktrees")); !os.IsNotExist(err) {
		t.Errorf("worktrees in the new folder: %v, want none", err)
	}
}
