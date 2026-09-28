//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The git.worktree_remove rows that need a permission deny, or a worktreeRoot (refused
// on Windows before any git call). Measured side by side against f6010b97 on a macOS
// VM. Every fixture lives under t.TempDir(), and every chmod is undone in a cleanup.

func chmodFor(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
}

// dirOrder is the order in which the file system lists dir, not sorted.
func dirOrder(t *testing.T, dir string) []string {
	t.Helper()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(-1)
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// A delete that fails part-way stops at the first entry that fails, in the order of
// the directory read. That entry, every entry after it, and `.git` stay. The entry and
// the branch stay too, and the reply names the entry only (rows P01 to P05). Before,
// `git worktree remove --force` deleted `.git` and the entry, and the reply quoted
// git's text.
func TestWorktreeRemovePartialDeleteKeepsGit(t *testing.T) {
	skipIfRoot(t)
	f := newRmFixture(t)
	for _, n := range []string{"q9.txt", "a1.txt", "m.txt", "a.txt"} {
		writeFile(t, filepath.Join(f.wt, n), "x\n", 0o644)
	}
	writeFile(t, filepath.Join(f.wt, "zz", "f.txt"), "x\n", 0o644)
	chmodFor(t, filepath.Join(f.wt, "zz"), 0o555)
	order := dirOrder(t, f.wt)
	raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
	wantRemoveError(t, raw, "failed to remove worktree: RemoveAll zz: permission denied")
	want := []string{".git"}
	for _, n := range order[slices.Index(order, "zz"):] {
		if n != ".git" {
			want = append(want, n)
		}
	}
	got := dirOrder(t, f.wt)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("left %v, want %v (read order %v)", got, want, order)
	}
	mustExist(t, filepath.Join(f.wt, "zz", "f.txt"))
	mustExist(t, f.entry())
	if !f.hasBranch("wt1") {
		t.Error("branch wt1 was deleted")
	}
}

// An entry that cannot be deleted after the tree is gone is reported with
// success:false. The branch is still deleted (rows D01 and D03). Before, the reply
// was success:true.
func TestWorktreeRemoveReportsEntryDropFailure(t *testing.T) {
	skipIfRoot(t)
	f := newRmFixture(t)
	chmodFor(t, f.entry(), 0o555)
	raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
	wantRemoveError(t, raw, "removed the worktree but could not drop its registration (RemoveAll wt1: permission denied)")
	mustBeGone(t, f.wt)
	mustExist(t, f.entry())
	if f.hasBranch("wt1") {
		t.Error("branch wt1 is still there")
	}
}

// A worktrees directory that exists but cannot be read blocks the removal with the
// lock-check text (row L02). An entry directory that cannot be read does not: the
// tree and the branch go and the entry stays (row L01). Before, L02 deleted the tree.
func TestWorktreeRemoveUnreadableRegistrations(t *testing.T) {
	skipIfRoot(t)
	t.Run("worktrees dir", func(t *testing.T) {
		f := newRmFixture(t)
		chmodFor(t, filepath.Dir(f.entry()), 0o000)
		raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
		wantRemoveError(t, raw, lockCheckRefusal(f.wt))
		mustExist(t, filepath.Join(f.wt, ".git"))
		if !f.hasBranch("wt1") {
			t.Error("branch wt1 was deleted")
		}
	})
	t.Run("entry dir", func(t *testing.T) {
		f := newRmFixture(t)
		chmodFor(t, f.entry(), 0o000)
		raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
		if raw != removeOK {
			t.Errorf("reply = %s, want %s", raw, removeOK)
		}
		mustBeGone(t, f.wt)
		mustExist(t, f.entry())
	})
}

// extFixture is f plus a worktreeRoot R holding one registered worktree R/proj/e0 on
// branch e0.
type extFixture struct {
	rmFixture
	root string
	e0   string
}

func (f extFixture) e0Entry() string { return filepath.Join(f.repo, ".git", "worktrees", "e0") }

func newExtFixture(t *testing.T) extFixture {
	t.Helper()
	f := newRmFixture(t)
	root := filepath.Join(filepath.Dir(f.repo), "R")
	e0 := filepath.Join(root, "proj", "e0")
	runGit(t, f.repo, "worktree", "add", "-q", "-b", "e0", e0)
	return extFixture{rmFixture: f, root: root, e0: e0}
}

func (f extFixture) remove(t *testing.T, wp, branch string) string {
	t.Helper()
	return removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": wp, "worktreeRoot": f.root, "branchName": branch})
}

func (f extFixture) notAWorktree(wp, reason string) string {
	return "refusing to remove worktree: " + wp + " is not a worktree of " + f.repo + " (" + reason +
		"), so it is left in place; remove it by hand if it is a leftover"
}

// With a worktreeRoot, a leaf that is a symbolic link or a regular file gets its own
// refusal, not the no-.git one (rows S03 and S04).
func TestWorktreeRemoveExternalLeafKinds(t *testing.T) {
	f := newExtFixture(t)
	proj := filepath.Dir(f.e0)
	lnk := filepath.Join(proj, "lnk")
	if err := os.Symlink(f.e0, lnk); err != nil {
		t.Fatal(err)
	}
	wantRemoveError(t, f.remove(t, lnk, ""), "refusing to remove worktree: "+lnk+" is a symbolic link, not a worktree directory")
	file := filepath.Join(proj, "f0")
	writeFile(t, file, "x\n", 0o644)
	wantRemoveError(t, f.remove(t, file, ""), "refusing to remove worktree: "+file+" is not a directory")
	mustExist(t, lnk)
	mustExist(t, file)
	mustExist(t, filepath.Join(f.e0, ".git"))
}

// With a worktreeRoot, an empty plain directory is removed and its branch deleted (rows
// E01 and E02). A plain directory that holds files is refused and left in place. The
// first path is the one sent, trailing slash and all (rows X03 and X04).
func TestWorktreeRemoveExternalPlainDirectory(t *testing.T) {
	f := newExtFixture(t)
	proj := filepath.Dir(f.e0)
	for _, wp := range []string{filepath.Join(proj, "empty"), filepath.Join(proj, "empty2") + "/"} {
		name := filepath.Base(filepath.Clean(wp))
		runGit(t, f.repo, "branch", name)
		if err := os.Mkdir(filepath.Clean(wp), 0o755); err != nil {
			t.Fatal(err)
		}
		if raw := f.remove(t, wp, name); raw != removeOK {
			t.Errorf("remove(%s) = %s, want %s", wp, raw, removeOK)
		}
		mustBeGone(t, wp)
		if f.hasBranch(name) {
			t.Errorf("branch %s is still there", name)
		}
	}
	p0 := filepath.Join(proj, "p0")
	writeFile(t, filepath.Join(p0, "keep.txt"), "x\n", 0o644)
	wantRemoveError(t, f.remove(t, p0+"/", ""), f.notAWorktree(p0+"/", p0+" has no .git file"))
	mustExist(t, filepath.Join(p0, "keep.txt"))
}

// With a worktreeRoot, a worktree of this repository whose whole worktrees directory
// is gone is a stale worktree. It is deleted with its branch (row E04). Before, the
// reply was the transient "could not verify" text.
func TestWorktreeRemoveExternalStaleWorktree(t *testing.T) {
	f := newExtFixture(t)
	if err := os.RemoveAll(filepath.Dir(f.e0Entry())); err != nil {
		t.Fatal(err)
	}
	if raw := f.remove(t, f.e0, "e0"); raw != removeOK {
		t.Errorf("reply = %s, want %s", raw, removeOK)
	}
	mustBeGone(t, f.e0)
	if f.hasBranch("e0") {
		t.Error("branch e0 is still there")
	}
}

// With a worktreeRoot, a worktree whose `.git` file is damaged but whose registration
// by path is locked answers the locked refusal (row E06). An entry that cannot be read
// gives the own-admin-directory reason (row L03).
func TestWorktreeRemoveExternalUnverified(t *testing.T) {
	t.Run("locked by path", func(t *testing.T) {
		f := newExtFixture(t)
		runGit(t, f.repo, "worktree", "lock", f.e0)
		writeFile(t, filepath.Join(f.e0, ".git"), "garbage\n", 0o644)
		wantRemoveError(t, f.remove(t, f.e0, "e0"), lockedWorktreeRefusal(f.e0))
		mustExist(t, filepath.Join(f.e0, ".git"))
	})
	t.Run("unreadable entry", func(t *testing.T) {
		skipIfRoot(t)
		f := newExtFixture(t)
		chmodFor(t, f.e0Entry(), 0o000)
		wantRemoveError(t, f.remove(t, f.e0, "e0"), f.notAWorktree(f.e0, f.e0+
			" carries a .git file that does not name this repository's own worktree admin directory"))
		mustExist(t, filepath.Join(f.e0, ".git"))
	})
	t.Run("gone and locked", func(t *testing.T) {
		f := newExtFixture(t)
		runGit(t, f.repo, "worktree", "lock", f.e0)
		if err := os.RemoveAll(f.e0); err != nil {
			t.Fatal(err)
		}
		wantRemoveError(t, f.remove(t, f.e0, "e0"), "refusing to remove worktree: "+f.e0+" is gone but its "+
			"registration is locked (git worktree lock); unlock it to remove the registration and branch")
		mustExist(t, f.e0Entry())
	})
	t.Run("unreadable .git file", func(t *testing.T) {
		// Not measured: a `.git` read error is not a refusal, so the reply is the
		// transient text, and nothing is deleted.
		skipIfRoot(t)
		f := newExtFixture(t)
		gitFile := filepath.Join(f.e0, ".git")
		if err := os.Chmod(gitFile, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(gitFile, 0o644) })
		got := removeErrorField(t, f.remove(t, f.e0, "e0"))
		want := "failed to remove worktree: could not verify that " + f.e0 + " is a worktree of " + f.repo + " ("
		if !strings.HasPrefix(got, want) || !strings.HasSuffix(got, "permission denied); retry") {
			t.Errorf("error = %q, want %q…permission denied); retry", got, want)
		}
		mustExist(t, gitFile)
	})
}
