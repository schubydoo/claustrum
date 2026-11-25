package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// TestRepoParentsInAReadDeniedRepo: a repo of mode 0300 has write and search
// permission and no read permission. The parent step still makes `.claude` and
// `.claude/worktrees` there, as a mkdir by path did (claustrum's own, not
// measured). Mutation: open the repo with O_RDONLY, and the step fails with
// `mkdirat .claude: permission denied`.
func TestRepoParentsInAReadDeniedRepo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this case relies on")
	}
	repo := t.TempDir()
	chmodForTest(t, repo, 0o300)
	dir := filepath.Join(repo, ".claude", "worktrees")
	if err := mkdirRepoWorktreeParents(repo, dir); err != nil {
		t.Fatalf("mkdirRepoWorktreeParents = %v, want nil", err)
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		t.Errorf(".claude/worktrees: %v, want a folder", err)
	}
}

// TestLeafPathEnds: the leaf keeps the name of worktreePath. A path that ends in
// `/.` makes nothing and reads `mkdirat .: no such file or directory`, as a mkdir
// by path did. A trailing slash makes the leaf. A path of PATH_MAX bytes or more
// reads `mkdirat <leaf>: file name too long`, also when its parent is shorter
// (claustrum's own, not measured on 87375cc7). Mutations: clean the path (the
// dot case makes wt), or drop the length test (the long leaf is made).
func TestLeafPathEnds(t *testing.T) {
	base := t.TempDir()
	dot := filepath.Join(base, "wt") + "/."
	err := mkdirWorktreeLeaf(dot)
	if err == nil || err.Error() != "mkdirat .: no such file or directory" {
		t.Errorf("mkdirWorktreeLeaf(%q) = %v, want mkdirat .: no such file or directory", dot, err)
	}
	if _, err := os.Lstat(filepath.Join(base, "wt")); !os.IsNotExist(err) {
		t.Errorf("wt after the dot path: %v, want none", err)
	}
	if err := mkdirWorktreeLeaf(filepath.Join(base, "ws") + "/"); err != nil {
		t.Errorf("mkdirWorktreeLeaf with a trailing slash = %v, want nil", err)
	}
	if fi, err := os.Lstat(filepath.Join(base, "ws")); err != nil || !fi.IsDir() {
		t.Errorf("ws: %v, want a folder", err)
	}

	parent := base
	for len(parent) < syscall.PathMax-300 {
		parent = filepath.Join(parent, strings.Repeat("p", 200))
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("w", syscall.PathMax-len(parent))
	long := filepath.Join(parent, name)
	if err := mkdirWorktreeLeaf(long); !errors.Is(err, syscall.ENAMETOOLONG) || err.Error() != "mkdirat "+name+": file name too long" {
		t.Errorf("mkdirWorktreeLeaf of %d bytes = %v, want mkdirat <leaf>: file name too long", len(long), err)
	}
	if _, err := os.Lstat(long); err == nil {
		t.Error("the long leaf was made")
	}
}
