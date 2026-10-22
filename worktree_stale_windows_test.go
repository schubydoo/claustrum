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

// The Windows step works on `worktrees` of the separate git directory that the
// .git file of repo names. 5fd08069 removes the stale entry gd\worktrees\old
// there, and the create succeeds (cell S1 of round tier3-02, Windows VM, 3
// runs). Both forms of the step run: by the path, and through the held git
// directory.
func TestStaleStepOfSeparateGitDir(t *testing.T) {
	setup := func(t *testing.T) (repo, gd, leaf, old string) {
		root := t.TempDir()
		repo, gd = filepath.Join(root, "repo"), filepath.Join(root, "gd")
		mustWrite(t, filepath.Join(repo, ".git"), "gitdir: "+gd+"\n")
		leaf = filepath.Join(repo, ".claude", "worktrees", "w1")
		old = filepath.Join(gd, "worktrees", "old")
		mustWrite(t, filepath.Join(old, "gitdir"), filepath.ToSlash(leaf)+"/.git\n")
		return repo, gd, leaf, old
	}
	t.Run("by the path", func(t *testing.T) {
		repo, _, leaf, old := setup(t)
		if !dropStaleWorktreeRegistration(repo, leaf, nil) {
			t.Error("the step reports no match, want one")
		}
		if exists(old) {
			t.Error("the stale entry of the separate git directory stays, want it gone")
		}
	})
	t.Run("through the held git directory", func(t *testing.T) {
		repo, gd, leaf, _ := setup(t)
		held := acceptGitDir(gd, gitDirIdentity(gd))
		if held == nil {
			t.Fatal("the separate git directory is not accepted")
		}
		t.Cleanup(held.release)
		removals := recordHeldRemovals(t, held.info)
		if !dropStaleWorktreeRegistration(repo, leaf, held) {
			t.Error("the step reports no match, want one")
		}
		if got := strings.Join(*removals, ","); got != "worktrees/old held" {
			t.Errorf("the step asked to remove %q, want the old registration of the held folder", got)
		}
	})
}

// The step by the path keeps a stale entry with a `locked` file, and both of
// two stale entries. 5fd08069 does so in the separate git directory gd, and
// the add then fails with the text of git (cells S1L and S1T of round
// tier3-05, Windows VM, 3 runs each).
func TestStaleStepByPathKeepsLockedAndTwinEntries(t *testing.T) {
	setup := func(t *testing.T) (repo, gd, leaf string) {
		root := t.TempDir()
		repo, gd = filepath.Join(root, "repo"), filepath.Join(root, "gd")
		mustWrite(t, filepath.Join(repo, ".git"), "gitdir: "+gd+"\n")
		return repo, gd, filepath.Join(repo, ".claude", "worktrees", "w1")
	}
	t.Run("S1L", func(t *testing.T) {
		repo, gd, leaf := setup(t)
		old := filepath.Join(gd, "worktrees", "old")
		mustWrite(t, filepath.Join(old, "gitdir"), filepath.ToSlash(leaf)+"/.git\n")
		mustWrite(t, filepath.Join(old, "locked"), "t lock\n")
		if !dropStaleWorktreeRegistration(repo, leaf, nil) {
			t.Error("the step reports no match, want one")
		}
		if !exists(filepath.Join(old, "locked")) {
			t.Error("the locked entry is gone, want it kept")
		}
	})
	t.Run("S1T", func(t *testing.T) {
		repo, gd, leaf := setup(t)
		for _, name := range []string{"old", "old2"} {
			mustWrite(t, filepath.Join(gd, "worktrees", name, "gitdir"), filepath.ToSlash(leaf)+"/.git\n")
		}
		if !dropStaleWorktreeRegistration(repo, leaf, nil) {
			t.Error("the step reports no match, want one")
		}
		for _, name := range []string{"old", "old2"} {
			if !exists(filepath.Join(gd, "worktrees", name, "gitdir")) {
				t.Errorf("the entry %s is gone, want both kept", name)
			}
		}
	})
}

// With a junction or a directory symbolic link at `.git\worktrees` to a folder
// T with a stale entry `old` and a file MARK, 5fd08069 changes nothing in T
// with "shadowDirOff":true (cells D1J and D1S of round tier3-05, Windows VM, 3
// runs each). The step by the path lists nothing behind the link. The step
// through the held git directory, with T inside it, does the same. That case
// is claustrum's own, not measured.
func TestStaleStepActsNothingBehindALink(t *testing.T) {
	for _, kind := range []struct {
		cell string
		link func(*testing.T, string, string)
	}{{"D1J", makeJunction}, {"D1S", makeDirSymlink}} {
		t.Run(kind.cell+" by the path", func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
			target := filepath.Join(root, "wt")
			mustWrite(t, filepath.Join(target, "old", "gitdir"), filepath.ToSlash(leaf)+"/.git\n")
			mustWrite(t, filepath.Join(target, "MARK"), "mark\n")
			if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			kind.link(t, filepath.Join(repo, ".git", "worktrees"), target)
			dropStaleWorktreeRegistration(repo, leaf, nil)
			for _, p := range []string{filepath.Join(target, "old", "gitdir"), filepath.Join(target, "MARK")} {
				if !exists(p) {
					t.Errorf("%s behind the link is gone, want it kept", p)
				}
			}
		})
		t.Run(kind.cell+" through the held git directory", func(t *testing.T) {
			f := newHeldFixture(t)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "gone")
			target := filepath.Join(f.gitDir, "T")
			if err := os.Rename(filepath.Join(f.gitDir, "worktrees"), target); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(target, "old", "gitdir"), filepath.ToSlash(leaf)+"/.git\n")
			kind.link(t, filepath.Join(f.gitDir, "worktrees"), target)
			if f.u.accepted.dropStaleRegistration(leaf) {
				t.Error("the step reports a match behind the link, want none")
			}
			if len(*f.removals) != 0 {
				t.Errorf("the step asked to remove %q behind the link, want nothing", *f.removals)
			}
		})
	}
}

// On Windows the create runs the stale step after `rev-parse
// --absolute-git-dir`, when the leaf folder is made. 5fd08069 removes the entry
// there (cell S1 of round tier3-02 and cell D1K of round tier3-05, Windows VM).
// The test reads the leaf when the step starts.
func TestStaleStepRunsAfterTheLeafIsMade(t *testing.T) {
	f := newWTFixture(t, false)
	gone := filepath.Join(t.TempDir(), "old")
	runGit(t, f.top, "worktree", "add", "-q", "-b", "oldb", gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(f.regDir, "old")
	mustWrite(t, filepath.Join(old, "gitdir"), filepath.ToSlash(f.leaf())+"/.git\n")
	srv, _ := newSeededShadowServer(t)
	prev := createStepHook
	t.Cleanup(func() { createStepHook = prev })
	ran, leafThere := false, false
	createStepHook = func(at string) {
		if at == "stale" {
			ran, leafThere = true, exists(f.leaf())
		}
	}
	raw, _ := f.create(t, srv, "w1", "", 0)
	if !ran {
		t.Fatalf("the step did not run with a held git directory, reply = %s", raw)
	}
	if !leafThere {
		t.Error("the leaf folder is not there when the step starts, want it made first")
	}
	if !strings.Contains(raw, `"success":true`) {
		t.Errorf("reply = %s\nwant success", raw)
	}
	if exists(old) {
		t.Error("the stale entry stays, want it removed")
	}
}
