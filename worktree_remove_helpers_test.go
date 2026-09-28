package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The edges of the git.worktree_remove helpers that no RPC fixture reaches cheaply.

// A missing repository, a missing component and a component that is a file all give
// the zero target: the worktree reads as gone.
func TestLocateWorktreeGoneShapes(t *testing.T) {
	base := resolveTestRoot(t, t.TempDir())
	writeFile(t, filepath.Join(base, "file"), "x\n", 0o644)
	for _, c := range []struct{ repo, wp string }{
		{filepath.Join(base, "missing"), filepath.Join(base, "missing", "a", "b")},
		{base, filepath.Join(base, "nope", "b")},
		{base, filepath.Join(base, "file", "b")},
	} {
		tg, err := locateInRepoWorktree(c.repo, c.wp)
		if err != nil || tg.parent != nil {
			tg.close()
			t.Errorf("locateInRepoWorktree(%s, %s) = (%v, %v), want the zero target", c.repo, c.wp, tg.parent, err)
		}
	}
	tg, err := locateExternalWorktree(filepath.Join(base, "nope", "b"))
	if err != nil || tg.parent != nil {
		tg.close()
		t.Errorf("locateExternalWorktree(missing dir) = (%v, %v), want the zero target", tg.parent, err)
	}
	tg, err = locateExternalWorktree(filepath.Join(base, "e0"))
	if err != nil || tg.parent == nil || tg.leaf != "e0" || tg.path != filepath.Join(base, "e0") {
		t.Errorf("locateExternalWorktree(existing dir) = (%+v, %v)", tg, err)
	}
	tg.close()
	if tg, err := openRemoveParent(filepath.Join(base, "missing"), ".", "x"); err != nil || tg.parent != nil {
		t.Errorf("openRemoveParent(missing) = (%v, %v), want the zero target", tg.parent, err)
	}
}

// A leaf that cannot be opened as a directory is removed by name through its parent,
// which never follows a symbolic link.
func TestDeleteWorktreeDirNonDirectoryLeaf(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "f"), "x\n", 0o644)
	parent, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if err := deleteWorktreeDir(parent, "f", filepath.Join(dir, "f")); err != nil {
		t.Fatal(err)
	}
	mustBeGone(t, filepath.Join(dir, "f"))
}

// A relative gitdir in a `.git` file is taken relative to the worktree.
func TestWorktreeGitFileTargetRelative(t *testing.T) {
	wp := t.TempDir()
	writeFile(t, filepath.Join(wp, ".git"), "gitdir: ../repo/.git/worktrees/w\n", 0o644)
	root, err := os.OpenRoot(wp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	got, err := worktreeGitFileTarget(root, wp)
	if want := filepath.Join(wp, "..", "repo", ".git", "worktrees", "w"); err != nil || got != want {
		t.Errorf("worktreeGitFileTarget = (%q, %v), want %q", got, err, want)
	}
	// Extra white space after "gitdir: " is dropped, so an absolute path stays
	// absolute rather than being joined onto the worktree.
	abs := filepath.Join(t.TempDir(), "repo", ".git", "worktrees", "w")
	writeFile(t, filepath.Join(wp, ".git"), "gitdir:   "+abs+"\n", 0o644)
	if got, err := worktreeGitFileTarget(root, wp); err != nil || got != abs {
		t.Errorf("worktreeGitFileTarget(extra space) = (%q, %v), want %q", got, err, abs)
	}
}

// Records that are empty or do not end in `.git` are skipped. A relative one is taken
// relative to its entry.
func TestLoadEntryRecordShapes(t *testing.T) {
	dir := t.TempDir()
	for name, rec := range map[string]string{"empty": " \n", "odd": "/x/y\n", "rel": "../../w/.git\n"} {
		writeFile(t, filepath.Join(dir, name, "gitdir"), rec, 0o644)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"empty", "odd", "none"} {
		if r, ok := loadEntryRecord(root, dir, name); ok {
			t.Errorf("record %s = %+v, want it skipped", name, r)
		}
	}
	r, ok := loadEntryRecord(root, dir, "rel")
	if want := filepath.Join(filepath.Dir(dir), "w"); !ok || r.path != want {
		t.Errorf("record rel = (%+v, %v), want path %s", r, ok, want)
	}
}

// Without a worktrees directory nothing is locked and the entry delete reports the
// missing directory.
func TestWorktreeEntryHelpersWithoutWorktreesDir(t *testing.T) {
	common := filepath.Join(t.TempDir(), ".git")
	if verifiedEntryLocked(common, "w") {
		t.Error("verifiedEntryLocked(no worktrees dir) = true")
	}
	if err := dropWorktreeEntry(common, "w"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dropWorktreeEntry(no worktrees dir) = %v, want not-exist", err)
	}
}

// entryAlreadyGone holds only for an entry of this repository that is gone.
func TestEntryAlreadyGone(t *testing.T) {
	common := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(filepath.Join(common, "worktrees", "here"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		gitDir string
		want   bool
	}{
		{"", false},
		{filepath.Join(common, "other", "w"), false},
		{filepath.Join(t.TempDir(), ".git", "worktrees", "w"), false},
		{filepath.Join(common, "worktrees", "here"), false},
		{filepath.Join(common, "worktrees", "gone"), true},
	} {
		if got := entryAlreadyGone(c.gitDir, common); got != c.want {
			t.Errorf("entryAlreadyGone(%q) = %v, want %v", c.gitDir, got, c.want)
		}
	}
	if !entryAlreadyGone(filepath.Join(common, "worktrees", "x"), filepath.Join(filepath.Dir(common), "none", "..", ".git")) {
		t.Error("entryAlreadyGone with an unclean common dir = false, want true")
	}
}
