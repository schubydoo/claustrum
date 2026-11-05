//go:build unix

package git

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The tests of the swap texts of a Windows create, run on Linux and macOS with
// the steps of Windows switched on. A symbolic link takes the place of the
// junction of the Windows rows. The texts are those of
// TestCreateStepsOfAHeldGitDirOnWindows (b3081b51, measured on Windows).

// setStagedAddWindowsSteps sets the switch of the Windows steps for one test.
func setStagedAddWindowsSteps(t *testing.T, on bool) {
	t.Helper()
	old := stagedAddWindowsSteps
	t.Cleanup(func() { stagedAddWindowsSteps = old })
	stagedAddWindowsSteps = on
}

// linkWorktreesAt puts a link to the folder worktrees of other at the folder
// worktrees of gitDir when the create reaches step, one time. The folder
// itself goes to `worktrees-real`.
func linkWorktreesAt(t *testing.T, step, gitDir, other string) {
	t.Helper()
	wt := filepath.Join(gitDir, worktreesSubdir)
	atStep(t, step, func() {
		if err := os.Rename(wt, wt+"-real"); err != nil {
			t.Errorf("the rename of worktrees: %v", err)
			return
		}
		linkAt(t, wt, filepath.Join(other, worktreesSubdir))
	})
}

// TestWindowsCreateSwapTexts: rows C, F and H of the Windows test. C and F
// are a link at `.git` or at `.git/worktrees` right after `rev-parse
// --absolute-git-dir`, and H is the link at `.git/worktrees` right before the
// `worktree add` call. Without the prefix `cannot add a worktree: ` of the
// refusal after the call, row C gets the old text. Without the test of the
// link at worktrees, row F gets the refusal of the registered leaf. Without
// the return of the last error of registrationTarget, row H gets the text of
// the hundred names.
func TestWindowsCreateSwapTexts(t *testing.T) {
	const realDir = `".git" is a link or a file where Claude needs a real directory; put the directory itself in its place`
	for _, row := range []string{"C a link at .git after the gitdir call", "F a link at worktrees after the gitdir call", "H a link at worktrees before the add"} {
		t.Run(row, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			mustWrite(t, filepath.Join(f.gitDir, "worktrees", "old8", "gitdir"), filepath.Join(leaf, ".git")+"\n")
			other := filepath.Join(resolveTestRoot(t, t.TempDir()), "T")
			if err := os.CopyFS(other, os.DirFS(f.gitDir)); err != nil {
				t.Fatal(err)
			}
			setStagedAddWindowsSteps(t, true)
			wt := filepath.Join(f.gitDir, worktreesSubdir)
			var want string
			switch row[0] {
			case 'C':
				swapAt(t, "answered", f.gitDir, other)
				want = "cannot add a worktree: cannot list the repository's worktree entries: " + realDir
			case 'F':
				linkWorktreesAt(t, "answered", f.gitDir, other)
				want = "cannot add a worktree: cannot prepare the repository's worktrees directory: " +
					quoteOperand(wt) + " is not a directory"
			default:
				stubStepsAt(t, "add", "worktree,add", "pre", []string{"rename", wt, wt + "-real"}, []string{"symlink", filepath.Join(other, worktreesSubdir), wt})
				want = completingAddPrefix + "statat " + filepath.Join("worktrees", "w199") + ": path escapes from parent"
			}
			raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			if text, code := createErrorField(t, raw); text != want || code != "worktree_add_failed" {
				t.Errorf("git.worktree_create = %s\nwant the error %s", raw, want)
			}
			if _, err := os.Lstat(filepath.Join(other, "worktrees", "old8", "gitdir")); err != nil {
				t.Errorf("old8 of T: %v, want it kept", err)
			}
		})
	}
}

// TestWindowsCreateLeafOfAnotherEntry: the staged leaf names the entry decoy
// of the git directory, as in TestCreateWithALeafOfAnotherEntryOnWindows
// (b3081b51, measured on Windows). The create answers otherStagedEntryText
// with worktree_add_failed, the leaf and the branch go, and the worktrees
// folder keeps its entries. Without the failedAdd arm of the create, or with
// failedAdd false, the frame has unsafe_path.
func TestWindowsCreateLeafOfAnotherEntry(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	decoy := filepath.Join(f.gitDir, worktreesSubdir, "decoy")
	mustWrite(t, filepath.Join(decoy, "gitdir"), "/nowhere/.git\n")
	before := entryNames(t, filepath.Join(f.gitDir, worktreesSubdir))
	setStagedAddWindowsSteps(t, true)
	atStep(t, "staged", func() {
		folder := daemonFolder(t, f.run)
		names := addFolders(t, folder)
		if len(names) != 1 {
			t.Errorf("the folder of the daemon holds %q, want one .s<n>", names)
			return
		}
		staged := filepath.Join(folder, names[0], stagedLeafFolder, "w1", ".git")
		if err := os.WriteFile(staged, []byte("gitdir: "+decoy+"\n"), 0o644); err != nil {
			t.Error(err)
		}
	})
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := `"result":{"success":false,"error":` + jsonQuoted(t, completingAddPrefix+otherStagedEntryText) + `,"errorCode":"worktree_add_failed"}}`
	if !strings.HasSuffix(raw, want) {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	for _, p := range []string{leaf, filepath.Join(f.gitDir, "refs", "heads", "w1")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s: %v, want it gone", p, err)
		}
	}
	if got := entryNames(t, filepath.Join(f.gitDir, worktreesSubdir)); !slices.Equal(got, before) {
		t.Errorf("the worktrees folder holds %q, want %q", got, before)
	}
}

// TestWindowsCreateHeldStagedLeaf (b3081b51, measured on Windows). Another
// process holds the staged leaf folder or `leaf`, so the read of its `.git`
// through the root at `.s<n>` fails. Mode 0o100 on the held folder stands in
// for the hold: the root open fails with EACCES, and a read by path works.
// Cases a and f1 complete the create. Cases b, c1, c3, d, e, f2 and f3 refuse
// it with otherStagedEntryText: a `.git` that names another entry, a failure
// that is no hold, an open of `leaf` that fails for no hold, a `.git` that is
// a link or too big, a staged leaf that is a link and a `leaf` that is a
// link. Case c2, a create without the Windows steps, gets the unsafe_path
// refusal of Linux and macOS.
func TestWindowsCreateHeldStagedLeaf(t *testing.T) {
	skipIfRoot(t)
	type stage struct{ folder, leaf, gitFile string }
	for _, tc := range []struct {
		name    string
		steps   bool
		parent  bool
		held    func(error) bool
		prepare func(t *testing.T, s stage)
		ok      bool
	}{
		{name: "a a held leaf", steps: true, ok: true},
		{name: "b a held leaf of another entry", steps: true, prepare: func(t *testing.T, s stage) {
			if err := os.WriteFile(s.gitFile, []byte("gitdir: /nowhere/.s9/worktrees/w1\n"), 0o644); err != nil {
				t.Error(err)
			}
		}},
		{name: "c1 a failure that is no hold", steps: true, held: func(error) bool { return false }},
		{name: "c2 no Windows steps", steps: false},
		{name: "c3 an open of leaf that fails for no hold", steps: true, parent: true, held: func(err error) bool {
			var pe *fs.PathError
			return errors.Is(err, os.ErrPermission) && (!errors.As(err, &pe) || pe.Path != stagedLeafFolder)
		}},
		{name: "d1 a link at .git", steps: true, prepare: func(t *testing.T, s stage) {
			moveAside(t, s.gitFile, filepath.Join(s.leaf, "real.git"))
			linkAt(t, s.gitFile, "real.git")
		}},
		{name: "d2 a .git over the bound", steps: true, prepare: func(t *testing.T, s stage) {
			if err := os.WriteFile(s.gitFile, []byte("gitdir: /"+strings.Repeat("x", repointMaxBytes)+"/"+filepath.Base(s.folder)+"/worktrees/w1\n"), 0o644); err != nil {
				t.Error(err)
			}
		}},
		{name: "e a link at the staged leaf", steps: true, prepare: func(t *testing.T, s stage) {
			moveAside(t, s.leaf, s.leaf+"-real")
			linkAt(t, s.leaf, "w1-real")
		}},
		{name: "f1 a held leaf folder", steps: true, parent: true, ok: true},
		{name: "f2 a held leaf folder with a link at the staged leaf", steps: true, parent: true, prepare: func(t *testing.T, s stage) {
			moveAside(t, s.leaf, s.leaf+"-real")
			linkAt(t, s.leaf, "w1-real")
		}},
		{name: "f3 a held leaf folder that is a link", steps: true, parent: true, prepare: func(t *testing.T, s stage) {
			moveAside(t, filepath.Dir(s.leaf), filepath.Dir(s.leaf)+"-real")
			linkAt(t, filepath.Dir(s.leaf), stagedLeafFolder+"-real")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			setStagedAddWindowsSteps(t, tc.steps)
			held := tc.held
			if held == nil {
				held = func(err error) bool { return errors.Is(err, os.ErrPermission) }
			}
			old := stagedLeafHeld
			t.Cleanup(func() { stagedLeafHeld = old })
			stagedLeafHeld = held
			atStep(t, "staged", func() {
				folder := daemonFolder(t, f.run)
				names := addFolders(t, folder)
				if len(names) != 1 {
					t.Errorf("the folder of the daemon holds %q, want one .s<n>", names)
					return
				}
				s := stage{folder: filepath.Join(folder, names[0])}
				s.leaf = filepath.Join(s.folder, stagedLeafFolder, "w1")
				s.gitFile = filepath.Join(s.leaf, ".git")
				if tc.prepare != nil {
					tc.prepare(t, s)
				}
				hold := s.leaf
				if tc.parent {
					hold = filepath.Dir(s.leaf)
				}
				if fi, err := os.Lstat(hold); err == nil && fi.Mode()&os.ModeSymlink != 0 {
					hold += "-real"
				}
				if err := os.Chmod(hold, 0o100); err != nil {
					t.Error(err)
				}
				t.Cleanup(func() { _ = os.Chmod(hold, 0o755) })
			})
			raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			reg := filepath.Join(f.gitDir, worktreesSubdir, "w1")
			if !tc.ok {
				want := `"result":{"success":false,"error":` + jsonQuoted(t, completingAddPrefix+otherStagedEntryText) + `,"errorCode":"worktree_add_failed"}}`
				if !tc.steps {
					want = `,"errorCode":"unsafe_path"}}`
				}
				if !strings.HasSuffix(raw, want) || !strings.Contains(raw, `"success":false`) {
					t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
				}
				for _, p := range []string{leaf, reg, filepath.Join(f.gitDir, "refs", "heads", "w1")} {
					if _, err := os.Lstat(p); !os.IsNotExist(err) {
						t.Errorf("%s: %v, want it gone", p, err)
					}
				}
				return
			}
			want := `"result":{"success":true,"path":` + jsonQuoted(t, leaf) + `,"sourceBranch":"main","branch":"w1"}}`
			if !strings.HasSuffix(raw, want) {
				t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
			}
			if got, err := os.ReadFile(filepath.Join(leaf, ".git")); err != nil || string(got) != worktreeGitFileText(f.gitDir, "w1") {
				t.Errorf("the .git file of the leaf = %q, %v", got, err)
			}
			if got, err := os.ReadFile(filepath.Join(reg, "gitdir")); err != nil || string(got) != stagedRecordText(leaf) {
				t.Errorf("the gitdir record = %q, %v, want %q", got, err, stagedRecordText(leaf))
			}
			if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "w1")); err != nil {
				t.Errorf("the branch w1: %v", err)
			}
		})
	}
}

// moveAside renames from to to, for a step of a test.
func moveAside(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Error(err)
	}
}

// TestWorktreesLinkRefusal: the text is "" for a request that holds no git
// directory, for a real folder and for a file at worktrees, and the text of
// the folder for a link there. Without the test of the kind of the entry the
// file gets the text.
func TestWorktreesLinkRefusal(t *testing.T) {
	f := newStagedSwapFixture(t)
	if got := (gitUse{}).worktreesLinkRefusal(); got != "" {
		t.Errorf("no held git directory = %q, want none", got)
	}
	if got := f.u.worktreesLinkRefusal(); got != "" {
		t.Errorf("a real folder at worktrees = %q, want none", got)
	}
	wt := filepath.Join(f.gitDir, worktreesSubdir)
	if err := os.Rename(wt, wt+"-real"); err != nil {
		t.Fatal(err)
	}
	linkAt(t, wt, wt+"-real")
	want := "cannot prepare the repository's worktrees directory: " + quoteOperand(wt) + " is not a directory"
	if got := f.u.worktreesLinkRefusal(); got != want {
		t.Errorf("a link at worktrees = %q, want %q", got, want)
	}
	if err := os.Remove(wt); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, wt, "a file\n")
	if got := f.u.worktreesLinkRefusal(); got != "" {
		t.Errorf("a file at worktrees = %q, want none", got)
	}
}

// TestWindowsStagedAddTexts: row D of the Windows steps in a staged add, a
// link at `.git` after the call. It gets the text of the config. Without the
// test of the kind of `.git` in finish, it gets the text of a real directory.
func TestWindowsStagedAddTexts(t *testing.T) {
	t.Run("D a link at .git after the call", func(t *testing.T) {
		f := newStagedSwapFixture(t)
		setStagedAddWindowsSteps(t, true)
		swapAt(t, "staged", f.gitDir, f.target.dir)
		_, stderr, err := stagedAdd(t, f.repoFolderFixture, f.u)
		want := "this repository's git config cannot be used: " + quoteOperand(filepath.Join(f.gitDir, "config")) +
			": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to"
		var incomplete *addIncomplete
		if !errors.As(err, &incomplete) || incomplete.text != want {
			t.Errorf("the add = %v, %q\nwant the error %s", err, stderr, want)
		}
		f.target.untouched(t)
		if len(*f.removals) != 0 {
			t.Errorf("the cleanup asked to remove %q, want nothing", *f.removals)
		}
	})
}
