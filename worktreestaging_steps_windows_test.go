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
	atStep(t, "added", func() { mustWrite(t, filepath.Join(f.staged(t), "w9", "HEAD"), "ref: refs/heads/main\n") })
	raw, _ := f.create(t)
	wantFailed(t, raw, completingAddPrefix+"git registered 2 worktrees where one was expected; try again", "worktree_add_failed")
	f.fullUndo(t)
}

// TestCreateWithALeafOfAnotherEntryOnWindows must match cell S2: a `.git` file
// of the leaf that names another entry of `.s<n>` refuses the create with
// unsafe_path, and the leaf and the branch go. Without step 2 the create
// succeeds.
func TestCreateWithALeafOfAnotherEntryOnWindows(t *testing.T) {
	f := newStepCreate(t)
	atStep(t, "added", func() {
		mustWrite(t, filepath.Join(f.leaf, ".git"), "gitdir: "+filepath.Join(f.staged(t), "w9")+"\n")
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
	atStep(t, "added", func() { mustWrite(t, filepath.Join(f.staged(t), "w1", "sub", "f.txt"), "x\n") })
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
	atStep(t, "added", func() {
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
	atStep(t, "added", func() { mustWrite(t, other, "x\n") })
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
	atStep(t, "added", func() {
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

// TestCreateWithAHeldLeafGitFileOnWindows follows cell S8v: the `.git` file of
// the leaf cannot be removed. Here a handle without delete sharing holds it,
// so the cause is a sharing violation where the cell has "Access is denied.".
// The create fails with the error of the remove and the undo clause. The moved
// registration is gone, and the branch and the leaf with its `.git` file stay.
// Without the remove of the moved registration it stays.
func TestCreateWithAHeldLeafGitFileOnWindows(t *testing.T) {
	f := newStepCreate(t)
	atStep(t, "added", func() {
		release, err := holdOpenErr(filepath.Join(f.leaf, ".git"), false)
		if err != nil {
			t.Errorf("the hold of the .git file: %v", err)
			return
		}
		t.Cleanup(release)
	})
	raw, _ := f.create(t)
	want := regexp.MustCompile(`"result":\{"success":false,"error":"git worktree add failed \(completing the new worktree's git files\): removeat \.git: [^"]+; and the undo could not finish for ` +
		regexp.QuoteMeta(jsonText(t, f.leaf)) + `: the worktree directory, its registration, and the branch all remain; remove them by hand before retrying \(RemoveAll \.git: [^"]+\)","errorCode":"worktree_add_failed"\}\}$`)
	if !want.MatchString(raw) {
		t.Errorf("git.worktree_create = %s\nwant a match of %s", raw, want)
	}
	gone(t, filepath.Join(f.gitDir, "worktrees", "w1"))
	if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "w1")); err != nil {
		t.Errorf("the branch w1: %v, want it kept", err)
	}
	if got := entryNames(t, f.leaf); !slices.Equal(got, []string{".git"}) {
		t.Errorf("the leaf holds %q, want .git only", got)
	}
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
	atStep(t, "added", func() {
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
