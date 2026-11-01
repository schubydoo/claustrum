//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// separateGitDirFixture moves the git directory of a new wtFixture to <root>/gd
// and puts a .git file `gitdir: <root>/gd` in its place. That is the layout of
// `git init --separate-git-dir`.
func separateGitDirFixture(t *testing.T) wtFixture {
	t.Helper()
	f := newWTFixture(t, false)
	gd := filepath.Join(f.realRoot(t), "gd")
	if err := os.Rename(filepath.Join(f.top, ".git"), gd); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.top, ".git"), "gitdir: "+gd+"\n", 0o644)
	f.regDir = filepath.Join(gd, worktreesSubdir)
	return f
}

// submoduleFixture moves the work tree of a new wtFixture to <root>/SP/m and its
// git directory to <root>/SP/.git/modules/m, with the relative .git file and the
// core.worktree item that `git submodule add` writes.
func submoduleFixture(t *testing.T) wtFixture {
	t.Helper()
	f := newWTFixture(t, false)
	sp := filepath.Join(f.realRoot(t), "SP")
	runGit(t, filepath.Dir(sp), "init", "-q", sp)
	mods := filepath.Join(sp, ".git", "modules")
	mkdirForTest(t, mods)
	if err := os.Rename(filepath.Join(f.top, ".git"), filepath.Join(mods, "m")); err != nil {
		t.Fatal(err)
	}
	m := filepath.Join(sp, "m")
	if err := os.Rename(f.top, m); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(m, ".git"), "gitdir: ../.git/modules/m\n", 0o644)
	runGit(t, m, "config", "core.worktree", "../../../m")
	f.top, f.base = m, m
	f.regDir = filepath.Join(mods, "m", worktreesSubdir)
	return f
}

// staleEntry makes the registration name of a worktree whose folder is gone,
// with branch <name>b, and then gives it the gitdir record.
func staleEntry(t *testing.T, f wtFixture, name, record string) string {
	t.Helper()
	gone := filepath.Join(f.realRoot(t), "gone-"+name, name)
	runGit(t, f.top, "worktree", "add", "-q", "-b", name+"b", gone)
	if err := os.RemoveAll(filepath.Dir(gone)); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(f.regDir, name)
	writeFile(t, filepath.Join(entry, "gitdir"), record+"\n", 0o644)
	return entry
}

// TestWorktreeCreateStaleEntryOfCommonGitDir pins the folder of the stale entry
// step for a baseRepo with a .git file. 5fd08069 works on `worktrees` of the
// common git directory (round tier3-02, 3 runs each):
//
//   - S1 (Linux, macOS and Windows VMs): a separate git directory gd. The one
//     stale entry gd/worktrees/old goes, and the create succeeds.
//   - S1M (Linux VM): a submodule. The entry of .git/modules/m/worktrees goes,
//     and the create succeeds.
//   - S1R (Linux VM): S1 with the relative record
//     ../../../<repo>/.claude/worktrees/w1/.git. It counts from the entry folder,
//     and the entry goes.
//   - S1L and S1T (Linux, macOS and Windows VMs): an entry with a `locked` file, and two
//     stale entries. Every entry stays, and the add fails. be1c1ca5 answers the
//     text of registeredLeafRefusal there (measured on Linux, macOS and Windows).
//
// Each cell runs with no settings control and in the state shadow, where the
// step goes through the held git directory.
func TestWorktreeCreateStaleEntryOfCommonGitDir(t *testing.T) {
	requireGit(t)
	record := func(f wtFixture) string { return filepath.Join(f.base, ".claude", "worktrees", "w1", ".git") }
	for _, tc := range []struct {
		name    string
		fixture func(*testing.T) wtFixture
		stage   func(*testing.T, wtFixture) []string // the stale entries that it made
		goes    bool
	}{
		{"S1", separateGitDirFixture, func(t *testing.T, f wtFixture) []string {
			return []string{staleEntry(t, f, "old", record(f))}
		}, true},
		{"S1M", submoduleFixture, func(t *testing.T, f wtFixture) []string {
			return []string{staleEntry(t, f, "old", record(f))}
		}, true},
		{"S1R", separateGitDirFixture, func(t *testing.T, f wtFixture) []string {
			rel := "../../../" + filepath.Base(f.base) + "/.claude/worktrees/w1/.git"
			return []string{staleEntry(t, f, "old", rel)}
		}, true},
		{"S1L", separateGitDirFixture, func(t *testing.T, f wtFixture) []string {
			e := staleEntry(t, f, "old", record(f))
			writeFile(t, filepath.Join(e, "locked"), "", 0o644)
			return []string{e}
		}, false},
		{"S1T", separateGitDirFixture, func(t *testing.T, f wtFixture) []string {
			return []string{staleEntry(t, f, "old", record(f)), staleEntry(t, f, "old2", record(f))}
		}, false},
	} {
		for _, state := range []string{"", " shadow"} {
			t.Run(tc.name+state, func(t *testing.T) {
				f := tc.fixture(t)
				entries := tc.stage(t, f)
				before := make([][]string, len(entries))
				for i, e := range entries {
					before[i] = regFiles(t, e)
				}
				srv := newTestServer(t)
				held := false
				if state != "" {
					srv, _ = newSeededShadowServer(t)
					old := createStepHook
					t.Cleanup(func() { createStepHook = old })
					createStepHook = func(at string) { held = held || at == "stale" }
				}
				raw, _ := f.create(t, srv, "w1", "", 0)
				if want := state != ""; held != want {
					t.Errorf("the step ran with a held git directory: %v, want %v", held, want)
				}
				if !tc.goes {
					if !strings.Contains(raw, `"error":"cannot add a worktree: `) || !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
						t.Errorf("reply = %s\nwant the refusal of a registered leaf", raw)
					}
					for i, e := range entries {
						if got := regFiles(t, e); !slices.Equal(got, before[i]) {
							t.Errorf("the entry %s = %q\nwant %q", filepath.Base(e), got, before[i])
						}
					}
					return
				}
				if !strings.Contains(raw, `"success":true`) {
					t.Errorf("reply = %s\nwant success", raw)
				}
				if got := f.regs(t); !slices.Equal(got, []string{"w1"}) {
					t.Errorf("registrations = %q, want w1 alone", got)
				}
				if _, err := os.Stat(filepath.Join(f.leaf(), "t.txt")); err != nil {
					t.Errorf("the leaf holds no checkout: %v", err)
				}
				if !f.hasRef(t, "oldb") {
					t.Error("refs/heads/oldb is gone, want it kept")
				}
			})
		}
	}
}
