//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// git.worktree_remove with a folder level that the daemon cannot read or search. The
// rows are the mode rows of 89cb6289 on Linux and macOS VMs (B1 to B18). This file is
// unix only: chmod denies nothing on Windows, and Windows refuses a worktreeRoot.

// restrictLevel sets the mode of dir for one request. The returned function puts 0755
// back and reports whether the request left the mode as set. The cleanup of chmodFor
// puts 0755 back too, so t.TempDir can delete the tree after a failed test.
func restrictLevel(t *testing.T, dir string, mode os.FileMode) (restore func()) {
	t.Helper()
	chmodFor(t, dir, mode)
	return func() {
		t.Helper()
		if fi, err := os.Lstat(dir); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("mode of %s after the request = %v (err=%v), want %v", dir, fi, err, mode)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// keptExternal fails the test unless the worktree e0, its registration and its branch
// are all there.
func (f extFixture) keptExternal(t *testing.T) {
	t.Helper()
	mustExist(t, filepath.Join(f.e0, ".git"))
	mustExist(t, f.e0Entry())
	if !f.hasBranch("e0") {
		t.Error("branch e0 was deleted")
	}
}

// Rows B3 and B4: a worktreeRoot of mode 0300 or 0100 is refused with the error of
// its open, and nothing is deleted. Before, the reply was success, and the worktree,
// its registration and its branch were deleted. Rows B5 and B6 (0000 and 0200) get the
// same text. Before, their text was "lstat <R>/proj: permission denied".
func TestWorktreeRemoveExternalRootWithoutReadBit(t *testing.T) {
	skipIfRoot(t)
	for _, mode := range []os.FileMode{0o300, 0o100, 0o000, 0o200} {
		t.Run(mode.String(), func(t *testing.T) {
			f := newExtFixture(t)
			restore := restrictLevel(t, f.root, mode)
			raw := f.remove(t, f.e0, "e0")
			restore()
			wantRemoveError(t, raw, "failed to remove worktree: open "+f.root+": permission denied")
			f.keptExternal(t)
		})
	}
}

// Row B18: the text names the resolved root, not the spelling of the request. The
// request goes through a symbolic link to the root here. On the macOS VM it went
// through /tmp, and the text named /private/tmp.
func TestWorktreeRemoveExternalRootTextIsResolved(t *testing.T) {
	skipIfRoot(t)
	f := newExtFixture(t)
	lnk := filepath.Join(filepath.Dir(f.root), "lnk")
	if err := os.Symlink(f.root, lnk); err != nil {
		t.Fatal(err)
	}
	restore := restrictLevel(t, f.root, 0o300)
	raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": filepath.Join(lnk, "proj", "e0"),
		"worktreeRoot": lnk, "branchName": "e0"})
	restore()
	wantRemoveError(t, raw, "failed to remove worktree: open "+f.root+": permission denied")
	f.keptExternal(t)
}

// The texts below the open of the root, level by level. A level with the read bit and
// without the search bit answers "statat .". A level without the read bit answers
// "openat <its name>". Nothing is deleted. The leaf rows gave these texts before too.
func TestWorktreeRemoveExternalLevelTexts(t *testing.T) {
	skipIfRoot(t)
	for _, c := range []struct {
		row   string
		level func(f extFixture) string
		mode  os.FileMode
		want  string
	}{
		{"B1 root 0600", func(f extFixture) string { return f.root }, 0o600, "statat ."},
		{"B2 root 0400", func(f extFixture) string { return f.root }, 0o400, "statat ."},
		{"B8 directory 0600", func(f extFixture) string { return filepath.Dir(f.e0) }, 0o600, "statat ."},
		{"B9 directory 0400", func(f extFixture) string { return filepath.Dir(f.e0) }, 0o400, "statat ."},
		{"B10 directory 0300", func(f extFixture) string { return filepath.Dir(f.e0) }, 0o300, "openat proj"},
		{"B11a directory 0100", func(f extFixture) string { return filepath.Dir(f.e0) }, 0o100, "openat proj"},
		{"B11b directory 0000", func(f extFixture) string { return filepath.Dir(f.e0) }, 0o000, "openat proj"},
		{"B13a leaf 0600", func(f extFixture) string { return f.e0 }, 0o600, "statat ."},
		{"B14a leaf 0300", func(f extFixture) string { return f.e0 }, 0o300, "openat e0"},
	} {
		t.Run(c.row, func(t *testing.T) {
			f := newExtFixture(t)
			restore := restrictLevel(t, c.level(f), c.mode)
			raw := f.remove(t, f.e0, "e0")
			restore()
			wantRemoveError(t, raw, "failed to remove worktree: "+c.want+": permission denied")
			f.keptExternal(t)
		})
	}
}

// Row B7: a worktreeRoot of mode 0500 does not stop the removal. The worktree, its
// registration and its branch go.
func TestWorktreeRemoveExternalReadOnlyRootIsRemoved(t *testing.T) {
	skipIfRoot(t)
	f := newExtFixture(t)
	restore := restrictLevel(t, f.root, 0o500)
	raw := f.remove(t, f.e0, "e0")
	restore()
	if raw != removeOK {
		t.Errorf("reply = %s, want %s", raw, removeOK)
	}
	mustBeGone(t, f.e0)
	mustBeGone(t, f.e0Entry())
	if f.hasBranch("e0") {
		t.Error("branch e0 is still there")
	}
}

// Rows B17a to B17d: without worktreeRoot, .claude and .claude/worktrees get the same
// texts, each with the name of its own level. Nothing is deleted. Before, the texts
// were "openat .claude/worktrees" and "statat wt1".
func TestWorktreeRemoveInRepoLevelTexts(t *testing.T) {
	skipIfRoot(t)
	for _, c := range []struct {
		row   string
		level string
		mode  os.FileMode
		want  string
	}{
		{"B17a .claude 0600", ".claude", 0o600, "statat ."},
		{"B17b .claude 0300", ".claude", 0o300, "openat .claude"},
		{"B17c worktrees 0600", filepath.Join(".claude", "worktrees"), 0o600, "statat ."},
		{"B17d worktrees 0300", filepath.Join(".claude", "worktrees"), 0o300, "openat worktrees"},
	} {
		t.Run(c.row, func(t *testing.T) {
			f := newRmFixture(t)
			restore := restrictLevel(t, filepath.Join(f.repo, c.level), c.mode)
			raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
			restore()
			wantRemoveError(t, raw, "failed to remove worktree: "+c.want+": permission denied")
			mustExist(t, filepath.Join(f.wt, ".git"))
			mustExist(t, f.entry())
			if !f.hasBranch("wt1") {
				t.Error("branch wt1 was deleted")
			}
		})
	}
}
