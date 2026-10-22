package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHeldStaleStepRules pins the rules of the Windows step through the held
// git directory (acceptedGitDir.dropStaleRegistration). 5fd08069 does so on a
// Windows VM (round tier3-05, 3 runs each):
//
//   - S1L: the one stale entry holds a `locked` file. It stays.
//   - S1T: two stale entries name the leaf. Both stay.
//
// The control is one stale entry, which goes. The last case is claustrum's
// own, not measured: `worktrees` is a link in the held folder, and nothing
// behind it goes. 5fd08069 changes nothing behind such a link with
// "shadowDirOff":true only (cells D1J and D1S). The step runs on Windows only.
// This test calls it on every system, with a symbolic link on Linux and macOS
// and a junction on Windows (linkAt).
func TestHeldStaleStepRules(t *testing.T) {
	record := func(f heldFixture) string {
		return filepath.ToSlash(filepath.Join(f.repo, ".claude", "worktrees", "gone")) + "/.git\n"
	}
	leaf := func(f heldFixture) string { return filepath.Join(f.repo, ".claude", "worktrees", "gone") }
	t.Run("one stale entry goes", func(t *testing.T) {
		f := newHeldFixture(t)
		mustWrite(t, filepath.Join(f.gitDir, "worktrees", "old", "gitdir"), record(f))
		if !f.u.accepted.dropStaleRegistration(leaf(f)) {
			t.Error("the step reports no match, want one")
		}
		if got := strings.Join(*f.removals, ","); got != "worktrees/old held" {
			t.Errorf("the step asked to remove %q, want worktrees/old of the held folder", got)
		}
	})
	t.Run("S1L a locked entry stays", func(t *testing.T) {
		f := newHeldFixture(t)
		mustWrite(t, filepath.Join(f.gitDir, "worktrees", "old", "gitdir"), record(f))
		mustWrite(t, filepath.Join(f.gitDir, "worktrees", "old", "locked"), "t lock\n")
		if !f.u.accepted.dropStaleRegistration(leaf(f)) {
			t.Error("the step reports no match, want one")
		}
		if len(*f.removals) != 0 {
			t.Errorf("the step asked to remove %q, want nothing", *f.removals)
		}
	})
	t.Run("S1T two stale entries stay", func(t *testing.T) {
		f := newHeldFixture(t)
		mustWrite(t, filepath.Join(f.gitDir, "worktrees", "old", "gitdir"), record(f))
		mustWrite(t, filepath.Join(f.gitDir, "worktrees", "old2", "gitdir"), record(f))
		if !f.u.accepted.dropStaleRegistration(leaf(f)) {
			t.Error("the step reports no match, want one")
		}
		if len(*f.removals) != 0 {
			t.Errorf("the step asked to remove %q, want nothing", *f.removals)
		}
	})
	t.Run("a link at worktrees", func(t *testing.T) {
		f := newHeldFixture(t)
		// The target is inside the held folder, so a root that follows the
		// link reaches it.
		target := filepath.Join(f.gitDir, "T")
		if err := os.Rename(filepath.Join(f.gitDir, "worktrees"), target); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(target, "old", "gitdir"), record(f))
		mustWrite(t, filepath.Join(target, "MARK"), "mark\n")
		linkAt(t, filepath.Join(f.gitDir, "worktrees"), target)
		if f.u.accepted.dropStaleRegistration(leaf(f)) {
			t.Error("the step reports a match behind the link, want none")
		}
		if len(*f.removals) != 0 {
			t.Errorf("the step asked to remove %q behind the link, want nothing", *f.removals)
		}
	})
}
