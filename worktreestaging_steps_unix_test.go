//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The steps of the staging move that need a folder mode or a rename of the
// moved registration during the move. They run on Linux and macOS.

// skipAsRoot skips a test whose folder of mode 0555 takes changes from root.
func skipAsRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("a folder of mode 0555 takes changes from root")
	}
}

// lockLeafAfterTheAdd makes the leaf of the fixture take no change of its
// entries after the add, so its `.git` file cannot be replaced.
func lockLeafAfterTheAdd(t *testing.T, f stepFixture) {
	t.Helper()
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	atStep(t, "staged", func() {
		if err := os.Chmod(leaf, 0o555); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() { _ = os.Chmod(leaf, 0o755) })
}

// swapAfterTheRename makes the stagingRename seam rename the moved
// registration w1 to w1x and put a new folder w1 with one file at its place,
// right after the rename.
func swapAfterTheRename(t *testing.T, f stepFixture) {
	t.Helper()
	old := stagingRename
	t.Cleanup(func() { stagingRename = old })
	stagingRename = func(root *os.Root, oldName, newName string) error {
		if err := root.Rename(oldName, newName); err != nil {
			return err
		}
		reg := filepath.Join(f.gitDir, "worktrees", "w1")
		if err := os.Rename(reg, reg+"x"); err != nil {
			return err
		}
		mustWrite(t, filepath.Join(reg, "f.txt"), "other\n")
		return nil
	}
}

// TestStagedMoveWithALeafThatTakesNoGitFile pins the failure of step 8 (cell
// S8v): the `.git` file of the leaf cannot be made, so the move fails with
// the error of that create and removes the moved registration again. Here the
// leaf folder takes no new entry. Without the remove the moved registration
// stays.
func TestStagedMoveWithALeafThatTakesNoGitFile(t *testing.T) {
	skipAsRoot(t)
	f := newStepFixture(t)
	lockLeafAfterTheAdd(t, f)
	_, _, err := stagedAdd(t, f.repoFolderFixture, f.u)
	if got, want := incompleteText(err), "openat .git: permission denied"; got != want {
		t.Errorf("the add = %v, want the text %q", err, want)
	}
	f.nothingMade(t)
	if f.u.accepted.staged.name != "" {
		t.Errorf("the moved entry = %+v, want none", f.u.accepted.staged)
	}
}

// TestStagedMoveKeepsAFolderSwappedInAfterTheRename pins step 7: the moved
// registration is renamed away right after the move, and a new folder w1
// takes its place. be1c1ca5 answers replacedEntryText (measured on Linux).
// claustrum removes neither folder (D24), and the leaf gets no `.git` file.
// Without the identity test the leaf names the new folder.
func TestStagedMoveKeepsAFolderSwappedInAfterTheRename(t *testing.T) {
	f := newStepFixture(t)
	swapAfterTheRename(t, f)
	leaf, _, err := stagedAdd(t, f.repoFolderFixture, f.u)
	if got := incompleteText(err); got != replacedEntryText {
		t.Errorf("the add = %v, want the text %q", err, replacedEntryText)
	}
	if got := mustRead(t, filepath.Join(f.gitDir, "worktrees", "w1", "f.txt")); got != "other\n" {
		t.Errorf("the folder at w1 = %q, want the swapped folder kept", got)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees", "w1x", "HEAD")); err != nil {
		t.Errorf("the moved registration at w1x: %v, want it kept", err)
	}
	if _, err := os.Lstat(filepath.Join(leaf, ".git")); !os.IsNotExist(err) {
		t.Errorf("the .git file of the leaf: %v, want none", err)
	}
}

// TestStagedRegistrationHasTheHeldIdentity: after a good move, the moved
// entry has the identity of the staging folder that the create held open. A
// folder that took its place later is not that entry, so the rollback and the
// index placement leave it (replacedIn). Without the held identity the new
// folder counts as the moved one.
func TestStagedRegistrationHasTheHeldIdentity(t *testing.T) {
	f := newStepFixture(t)
	if _, stderr, err := stagedAdd(t, f.repoFolderFixture, f.u); err != nil {
		t.Fatalf("the add failed: %v, %s", err, stderr)
	}
	moved := f.u.stagedRegistration()
	if moved.path != filepath.Join(f.gitDir, "worktrees", "w1") || moved.info == nil {
		t.Fatalf("the moved entry = %+v", moved)
	}
	reg := filepath.Join(f.gitDir, "worktrees", "w1")
	if err := os.Rename(reg, reg+"x"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(reg, "f.txt"), "other\n")
	root, err := os.OpenRoot(filepath.Join(f.gitDir, "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if !moved.replacedIn(root, "w1") {
		t.Error("the swapped folder w1 counts as the moved registration")
	}
	if moved.replacedIn(root, "w1x") {
		t.Error("the moved registration at w1x does not count as moved")
	}
}

// TestRefusedCreateDeletesNoRegistration pins the rollback of addRefused (cell
// S2 of round task087-01): the `.git` file of the leaf names a registration
// whose record names the leaf, and the rollback still deletes no
// registration. The leaf goes. Without noRegistration the rollback follows the
// `.git` file and deletes that registration.
func TestRefusedCreateDeletesNoRegistration(t *testing.T) {
	f := newStepFixture(t)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	reg := filepath.Join(f.gitDir, "worktrees", "w8")
	mustWrite(t, filepath.Join(reg, "gitdir"), filepath.Join(leaf, ".git")+"\n")
	mustWrite(t, filepath.Join(reg, "HEAD"), "ref: refs/heads/main\n")
	mustWrite(t, filepath.Join(leaf, ".git"), "gitdir: "+reg+"\n")
	cp := checkpointCreatedWorktree(leaf)
	t.Cleanup(cp.release)
	undoFailedCheckout(f.u, f.repo, leaf, "", cp, testedRegistration{noRegistration: true})
	if _, err := os.Lstat(filepath.Join(reg, "HEAD")); err != nil {
		t.Errorf("the registration w8: %v, want it kept", err)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
}

// TestRemoveOwnStagingLogsAFailedRemove: a staging folder that cannot be
// removed stays, and one line is logged. Its content can go. Here the git directory takes no
// change of its entries.
func TestRemoveOwnStagingLogsAFailedRemove(t *testing.T) {
	skipAsRoot(t)
	gitDir := t.TempDir()
	name := stagingNamePrefix + "0123456789abcdef"
	mustWrite(t, filepath.Join(gitDir, name, "HEAD"), "x\n")
	if err := os.Chmod(gitDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })
	logs := captureLogBuf(t)
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	st := &addStage{u: gitUse{gitDir: gitDir}, git: root}
	st.removeOwnStaging(name)
	if fi, err := os.Lstat(filepath.Join(gitDir, name)); err != nil || !fi.IsDir() {
		t.Errorf("the staging folder: %v, want it kept", err)
	}
	if !strings.Contains(logs.String(), "the staging folder "+name+" of the git folder was not removed") {
		t.Errorf("no log line for the failed remove: %q", logs.String())
	}
}

// TestRemoveMovedKeepsASwappedFolder: removeMoved removes the moved entry
// only while it is the staging folder that the move held. A folder that took
// its name stays (D24). Without the identity test it goes.
func TestRemoveMovedKeepsASwappedFolder(t *testing.T) {
	gitDir := t.TempDir()
	reg := filepath.Join(gitDir, "worktrees", "w1")
	mustWrite(t, filepath.Join(reg, "HEAD"), "ref: refs/heads/w1\n")
	moved, err := os.Stat(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(reg, reg+"x"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(reg, "f.txt"), "other\n")
	st := &addStage{u: gitUse{gitDir: gitDir}, git: openTestRoot(t, gitDir)}
	st.removeMoved(filepath.Join("worktrees", "w1"), moved)
	if got := mustRead(t, filepath.Join(reg, "f.txt")); got != "other\n" {
		t.Errorf("the folder at w1 = %q, want it kept", got)
	}
	if _, err := os.Lstat(filepath.Join(reg+"x", "HEAD")); err != nil {
		t.Errorf("the moved folder at w1x: %v, want it kept", err)
	}
}
