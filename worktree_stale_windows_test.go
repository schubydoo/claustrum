//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The stale step on Windows compares the record with the leaf without regard
// to letter case. 5fd08069 removes the entry `old` whose record differs from
// the leaf in one name or in the whole path (cells R-Q4i and R-Q4i2 of round
// task084-01, Windows VM, 3 of 3 runs each), and with the exact case too (cell
// R-Q4i0). A record of another path stays.
func staleCaseRecords(leaf string) map[string]string {
	slashed := filepath.ToSlash(leaf)
	dir, name := filepath.Dir(slashed), filepath.Base(slashed)
	return map[string]string{
		"R-Q4i0 exact case":       slashed + "/.git",
		"R-Q4i one name":          filepath.ToSlash(dir) + "/" + strings.ToUpper(name) + "/.git",
		"R-Q4i2 whole path upper": strings.ToUpper(slashed) + "/.git",
	}
}

func TestStaleStepIgnoresLetterCase(t *testing.T) {
	repo := t.TempDir()
	leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
	if err := os.MkdirAll(filepath.Dir(leaf), 0o755); err != nil {
		t.Fatal(err)
	}
	for cell, record := range staleCaseRecords(leaf) {
		t.Run(cell, func(t *testing.T) {
			old := filepath.Join(repo, ".git", "worktrees", "old")
			mustWrite(t, filepath.Join(old, "gitdir"), record+"\n")
			other := filepath.Join(repo, ".git", "worktrees", "other")
			mustWrite(t, filepath.Join(other, "gitdir"), filepath.ToSlash(filepath.Join(repo, "elsewhere"))+"/.git\n")
			if !dropStaleWorktreeRegistration(repo, leaf, nil) {
				t.Error("the step reports no match, want one")
			}
			if exists(old) {
				t.Errorf("the stale entry with the record %q stays, want it gone", record)
			}
			if !exists(other) {
				t.Error("the entry of another path is gone, want it kept")
			}
			if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The held form of the step has the same compare (acceptedGitDir.dropStaleRegistration).
func TestHeldStaleStepIgnoresLetterCase(t *testing.T) {
	for cell, record := range map[string]func(string) string{
		"R-Q4i one name": func(leaf string) string {
			return filepath.ToSlash(filepath.Dir(leaf)) + "/" + strings.ToUpper(filepath.Base(leaf)) + "/.git"
		},
		"R-Q4i2 whole path upper": func(leaf string) string { return strings.ToUpper(filepath.ToSlash(leaf)) + "/.git" },
	} {
		t.Run(cell, func(t *testing.T) {
			f := newHeldFixture(t)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "gone")
			mustWrite(t, filepath.Join(f.gitDir, "worktrees", "old", "gitdir"), record(leaf)+"\n")
			if !f.u.accepted.dropStaleRegistration(leaf) {
				t.Error("the step reports no match, want one")
			}
			if got := strings.Join(*f.removals, ","); got != "worktrees/old held" {
				t.Errorf("the step asked to remove %q, want the old registration of the held folder", got)
			}
		})
	}
}

// D27: on Windows the stale step keeps an entry whose record names a `.git`
// file that exists, in another letter case than the leaf. 5fd08069 deletes it
// (cell W-CS of round task084-02, Windows VM). Both forms of the step run: by
// the path, and through the held git directory.
func TestStaleStepKeepsALiveRecord(t *testing.T) {
	t.Run("by the path", func(t *testing.T) {
		repo := t.TempDir()
		live := filepath.Join(repo, ".claude", "worktrees", "W1")
		mustWrite(t, filepath.Join(live, ".git"), "gitdir: x\n")
		entry := filepath.Join(repo, ".git", "worktrees", "W1")
		mustWrite(t, filepath.Join(entry, "gitdir"), filepath.ToSlash(live)+"/.git\n")
		if !dropStaleWorktreeRegistration(repo, filepath.Join(repo, ".claude", "worktrees", "w1"), nil) {
			t.Error("the step reports no match, want one")
		}
		if !exists(entry) {
			t.Error("the entry of the live worktree W1 is gone, want it kept")
		}
	})
	t.Run("through the held git directory", func(t *testing.T) {
		f := newHeldFixture(t)
		live := filepath.Join(f.repo, ".claude", "worktrees", "W9")
		mustWrite(t, filepath.Join(live, ".git"), "gitdir: x\n")
		mustWrite(t, filepath.Join(f.gitDir, "worktrees", "W9", "gitdir"), filepath.ToSlash(live)+"/.git\n")
		if !f.u.accepted.dropStaleRegistration(filepath.Join(f.repo, ".claude", "worktrees", "w9")) {
			t.Error("the step reports no match, want one")
		}
		if len(*f.removals) != 0 {
			t.Errorf("the step asked to remove %q, want nothing", *f.removals)
		}
	})
}
