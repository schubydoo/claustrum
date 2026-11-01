//go:build windows

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The Windows frames of the staging step of git.worktree_create, as on
// 5fd08069 in round task087-01 (Windows VM). No test of this file ran on the
// host of the build: a Windows run must match the cell that each test names.
// The step hooks of the create (atStep) stand in for the wrapper of the round.

// stepCreate is a seeded shadow fixture after its first git.info, with the
// leaf of a create of the branch w1.
type stepCreate struct {
	shadowFixture
	leaf string
}

func newStepCreate(t *testing.T) stepCreate {
	t.Helper()
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	return stepCreate{shadowFixture: f, leaf: filepath.Join(f.repo, ".claude", "worktrees", "w1")}
}

func (f stepCreate) create(t *testing.T) (string, []envCall) {
	t.Helper()
	return f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": f.leaf})
}

// staged is the folder `worktrees` of the one `.s<n>` of the daemon.
func (f stepCreate) staged(t *testing.T) string {
	t.Helper()
	folder := daemonFolder(t, f.run)
	names := addFolders(t, folder)
	if len(names) != 1 {
		t.Fatalf("the folder of the daemon holds %q, want one .s<n>", names)
	}
	return filepath.Join(folder, names[0], worktreesSubdir)
}

// wantFailed tests that raw is the failed frame with errText and code.
func wantFailed(t *testing.T, raw, errText, code string) {
	t.Helper()
	want := `"result":{"success":false,"error":` + jsonQuoted(t, errText) + `,"errorCode":"` + code + `"}}`
	if !strings.HasSuffix(raw, want) {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
}

// gone tests that each path is not there.
func gone(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s: %v, want it not there", p, err)
		}
	}
}

// fullUndo tests the end state of a full rollback: no leaf, no registration
// w1, no branch w1 and no staging folder.
func (f stepCreate) fullUndo(t *testing.T) {
	t.Helper()
	gone(t, f.leaf, filepath.Join(f.gitDir, "worktrees", "w1"), filepath.Join(f.gitDir, "refs", "heads", "w1"))
	if got := stagingEntries(t, f.gitDir); len(got) != 0 {
		t.Errorf("the staging entries of the git directory = %q, want none", got)
	}
}

// TestCreateWithTwoStagedNamesOnWindows must match cell S1: a second new name
// in `.s<n>\worktrees` fails the create. Without step 1 the create succeeds.
func TestCreateWithTwoStagedNamesOnWindows(t *testing.T) {
	f := newStepCreate(t)
	atStep(t, "staged", func() { mustWrite(t, filepath.Join(f.staged(t), "w9", "HEAD"), "ref: refs/heads/main\n") })
	raw, _ := f.create(t)
	wantFailed(t, raw, completingAddPrefix+"git registered 2 worktrees where one was expected; try again", "worktree_add_failed")
	f.fullUndo(t)
}

// TestCreateWithALeafOfAnotherEntryOnWindows must match cell S2: a `.git` file
// of the staged leaf that names another entry of `.s<n>` refuses the create
// with unsafe_path, and the leaf and the branch go. Without step 2 the create
// succeeds.
func TestCreateWithALeafOfAnotherEntryOnWindows(t *testing.T) {
	f := newStepCreate(t)
	atStep(t, "staged", func() {
		staged := filepath.Join(filepath.Dir(f.staged(t)), stagedLeafFolder, "w1", ".git")
		if err := os.WriteFile(staged, []byte("gitdir: "+filepath.Join(f.staged(t), "w9")+"\n"), 0o644); err != nil {
			t.Error(err)
		}
	})
	raw, _ := f.create(t)
	wantFailed(t, raw, otherEntryRefusal(f.leaf), "unsafe_path")
	f.fullUndo(t)
}

// TestCreateCopiesTheStagedTreeOnWindows must match cell S3: a file in a sub
// folder of the staged registration is in the registration. Without the copy
// of the tree the sub folder is empty.
func TestCreateCopiesTheStagedTreeOnWindows(t *testing.T) {
	f := newStepCreate(t)
	atStep(t, "staged", func() { mustWrite(t, filepath.Join(f.staged(t), "w1", "sub", "f.txt"), "x\n") })
	raw, _ := f.create(t)
	if !strings.Contains(raw, `"result":{"success":true,`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	if got := mustRead(t, filepath.Join(f.gitDir, "worktrees", "w1", "sub", "f.txt")); got != "x\n" {
		t.Errorf("sub\\f.txt of the registration = %q", got)
	}
}

// TestCreateWithAJunctionInTheStagedEntryOnWindows must match cell S4: a
// junction in the staged registration fails the create with `openat j: path
// escapes from parent`. The target of the junction keeps its file. Without the
// open of each entry the junction is passed over and the create succeeds.
func TestCreateWithAJunctionInTheStagedEntryOnWindows(t *testing.T) {
	f := newStepCreate(t)
	tgt := filepath.Join(t.TempDir(), "tgt")
	atStep(t, "staged", func() {
		makeJunction(t, filepath.Join(f.staged(t), "w1", "j"), tgt)
		mustWrite(t, filepath.Join(tgt, "keep.txt"), "keep\n")
	})
	raw, _ := f.create(t)
	wantFailed(t, raw, completingAddPrefix+"openat j: path escapes from parent", "worktree_add_failed")
	f.fullUndo(t)
	if got := mustRead(t, filepath.Join(tgt, "keep.txt")); got != "keep\n" {
		t.Errorf("the file of the junction target = %q", got)
	}
}

// TestCreateTakesTheNextNameOnWindows must match cell S5 (C4d of round
// task007-01). A folder w1 that is made after the add sends the move to w11.
// Then the create is refused with unsafe_path, with no rollback. The
// leaf keeps only its `.git` file, which names w11. w1, w11 and the branch
// stay. Without registrationTarget the move refuses w1 and the create
// succeeds. Without step 5 the create succeeds with w11.
func TestCreateTakesTheNextNameOnWindows(t *testing.T) {
	f := newStepCreate(t)
	other := filepath.Join(f.gitDir, "worktrees", "w1", worktreeConfigName)
	atStep(t, "staged", func() { mustWrite(t, other, "x\n") })
	raw, calls := f.create(t)
	wantFailed(t, raw, otherEntryRefusal(f.leaf), "unsafe_path")
	if got := mustRead(t, filepath.Join(f.leaf, ".git")); got != worktreeGitFileText(f.gitDir, "w11") {
		t.Errorf("the .git file of the leaf = %q, want the text of w11", got)
	}
	if got := entryNames(t, f.leaf); !slices.Equal(got, []string{".git"}) {
		t.Errorf("the leaf holds %q, want .git only", got)
	}
	if got := mustRead(t, other); got != "x\n" {
		t.Errorf("the file of the other w1 = %q", got)
	}
	for _, p := range []string{filepath.Join(f.gitDir, "worktrees", "w11", "HEAD"), filepath.Join(f.gitDir, "refs", "heads", "w1")} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s: %v, want it kept", p, err)
		}
	}
	if ranReadTree(calls) {
		t.Error("a read-tree call ran")
	}
}

// TestCreateWithAHundredNamesOnWindows must match cell S6v. With w1 and w11 to
// w199 made after the add the create fails with the text of the hundred names.
// The 100 folders keep their files. Without registrationTarget the create
// succeeds.
func TestCreateWithAHundredNamesOnWindows(t *testing.T) {
	f := newStepCreate(t)
	names := []string{"w1"}
	for i := 1; i < 100; i++ {
		names = append(names, "w1"+strconv.Itoa(i))
	}
	atStep(t, "staged", func() {
		for _, n := range names {
			mustWrite(t, filepath.Join(f.gitDir, "worktrees", n, "f.txt"), "x\n")
		}
	})
	raw, _ := f.create(t)
	wantFailed(t, raw, completingAddPrefix+`the repository already has a hundred worktrees called "w1"; remove some (git worktree prune, git worktree remove)`, "worktree_add_failed")
	gone(t, f.leaf, filepath.Join(f.gitDir, "refs", "heads", "w1"))
	for _, n := range names {
		if got := entryNames(t, filepath.Join(f.gitDir, "worktrees", n)); !slices.Equal(got, []string{"f.txt"}) {
			t.Errorf("the folder %s holds %q", n, got)
		}
	}
	if got := stagingEntries(t, f.gitDir); len(got) != 0 {
		t.Errorf("the staging entries of the git directory = %q, want none", got)
	}
}

// TestCreateWithAPlantedLeafGitFileOnWindows: a `.git` put into the leaf
// after the add refuses the create with plantedGitFileText, and the leaf with
// that file and the branch go. be1c1ca5 answers so on Linux. Windows is
// claustrum's own, not measured. Without the test the create succeeds.
func TestCreateWithAPlantedLeafGitFileOnWindows(t *testing.T) {
	f := newStepCreate(t)
	atStep(t, "staged", func() { mustWrite(t, filepath.Join(f.leaf, ".git"), "gitdir: C:/planted\n") })
	raw, _ := f.create(t)
	wantFailed(t, raw, plantedGitFileText(f.leaf), "unsafe_path")
	f.fullUndo(t)
}

// TestCreateWithAHeldLockedFileOnWindows: a handle without delete sharing
// holds `locked` of the moved registration. A hold that ends inside the retry
// bound passes. A hold past it fails the create with `removeat locked: <cause>;
// try again`, and the entry stays. be1c1ca5 passes a 1 s hold and fails a 3 s
// hold (measured on Windows). Without the retry the short hold fails.
func TestCreateWithAHeldLockedFileOnWindows(t *testing.T) {
	hold := func(t *testing.T, f stepCreate, keep time.Duration) {
		old := stagingRename
		t.Cleanup(func() { stagingRename = old })
		stagingRename = func(root *os.Root, oldName, newName string) error {
			if err := old(root, oldName, newName); err != nil {
				return err
			}
			release, err := holdOpenErr(filepath.Join(f.gitDir, "worktrees", "w1", "locked"), false)
			if err != nil {
				return err
			}
			if keep == 0 {
				t.Cleanup(release)
				return nil
			}
			time.AfterFunc(keep, release)
			return nil
		}
	}
	t.Run("a short hold", func(t *testing.T) {
		f := newStepCreate(t)
		setRenameRetryBound(t, 2*time.Second)
		hold(t, f, 200*time.Millisecond)
		raw, _ := f.create(t)
		if !strings.Contains(raw, `"success":true`) {
			t.Errorf("git.worktree_create = %s, want success", raw)
		}
		gone(t, filepath.Join(f.gitDir, "worktrees", "w1", "locked"))
	})
	t.Run("a hold past the bound", func(t *testing.T) {
		f := newStepCreate(t)
		setRenameRetryBound(t, 100*time.Millisecond)
		hold(t, f, 0)
		raw, _ := f.create(t)
		want := regexp.MustCompile(`"result":\{"success":false,"error":"git worktree add failed \(completing the new worktree's git files\): removeat locked: [^"]+; try again","errorCode":"worktree_add_failed"\}\}$`)
		if !want.MatchString(raw) {
			t.Errorf("git.worktree_create = %s\nwant a match of %s", raw, want)
		}
		if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees", "w1", "locked")); err != nil {
			t.Errorf("locked of the entry: %v, want it kept", err)
		}
		gone(t, f.leaf)
	})
}

// TestRollbackRemovesTheMovedRegistrationOnWindows must match cell S14a: the
// gitdir record of the registration names another path during a checkout
// that fails. The rollback removes the registration that the move made, by
// its identity, and the other path stays. Without the moved registration in
// the rollback the registration stays.
func TestRollbackRemovesTheMovedRegistrationOnWindows(t *testing.T) {
	f := newStepCreate(t)
	other := filepath.Join(t.TempDir(), "other")
	mustWrite(t, filepath.Join(other, "keep.txt"), "keep\n")
	atStep(t, "checkout", func() {
		mustWrite(t, filepath.Join(f.gitDir, "worktrees", "w1", "gitdir"), filepath.ToSlash(filepath.Join(other, ".git"))+"\n")
	})
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "1")
	slowGit(t, "read-tree", "fail", 0, `wrapper: forced failure\n`, "")
	raw, _ := f.create(t)
	wantFailed(t, raw, "git worktree add failed (checkout): wrapper: forced failure", "worktree_add_failed")
	f.fullUndo(t)
	if got := mustRead(t, filepath.Join(other, "keep.txt")); got != "keep\n" {
		t.Errorf("the file of the other path = %q", got)
	}
}

// TestCreateWithARelativeRecordOnWindows must match cells S14 and S14n: a
// relative gitdir record of the staged registration names another path after
// the move. The create is refused with adminRecordRefusal and removes
// nothing: the leaf keeps its `.git` file, and the registration and the branch
// stay. No read-tree runs. Without the record test the create succeeds.
func TestCreateWithARelativeRecordOnWindows(t *testing.T) {
	f := newStepCreate(t)
	atStep(t, "staged", func() {
		reg := filepath.Join(f.staged(t), "w1")
		rel, err := filepath.Rel(reg, filepath.Join(f.leaf, ".git"))
		if err != nil {
			t.Error(err)
			return
		}
		mustWrite(t, filepath.Join(reg, "gitdir"), filepath.ToSlash(rel)+"\n")
	})
	raw, calls := f.create(t)
	wantFailed(t, raw, adminRecordRefusal(f.leaf), "unsafe_path")
	if got := entryNames(t, f.leaf); !slices.Equal(got, []string{".git"}) {
		t.Errorf("the leaf holds %q, want .git only", got)
	}
	for _, p := range []string{filepath.Join(f.gitDir, "worktrees", "w1", "HEAD"), filepath.Join(f.gitDir, "refs", "heads", "w1")} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s: %v, want it kept", p, err)
		}
	}
	if ranReadTree(calls) {
		t.Error("a read-tree call ran")
	}
}

// TestGitDirRenamesAfterTheStagingMoveOnWindows: after the staging step
// returns, the create holds no handle below `.git` but the held git directory,
// which shares delete access. So another process can rename `.git` before the
// reply. Here the rename and the rename back run at the step before the
// `config.worktree` file, and the create succeeds. Round task087-03 (Windows
// VM) saw "Access is denied." for that rename while the staging folder stayed
// open until the reply.
func TestGitDirRenamesAfterTheStagingMoveOnWindows(t *testing.T) {
	f := newStepCreate(t)
	atStep(t, "registration", func() {
		if err := os.Rename(f.gitDir, f.gitDir+"-real"); err != nil {
			t.Errorf("the rename of .git: %v", err)
			return
		}
		if err := os.Rename(f.gitDir+"-real", f.gitDir); err != nil {
			t.Errorf("the rename back: %v", err)
		}
	})
	raw, _ := f.create(t)
	if !strings.Contains(raw, `"result":{"success":true,`) {
		t.Errorf("git.worktree_create = %s, want success", raw)
	}
}
