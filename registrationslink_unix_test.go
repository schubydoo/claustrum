//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The guard tests of the rule that no delete of a remove or of a create goes
// behind a registrations folder that is a symbolic link (registrationsLinked,
// openRegistrationsFolder, acceptedGitDir.deleteRoot). The rule follows cell C3
// of round tier0-24 (Linux VM). For a create it is not measured. Each test
// works in temp folders only: R/.git/worktrees is a link to T, and T holds the
// entry w1 whose record names the leaf R/.claude/worktrees/w1.

// linkedRegistrations makes that state. It returns the repository, the leaf
// and T.
func linkedRegistrations(t *testing.T) (repo, leaf, target string) {
	t.Helper()
	root := realTempDir(t)
	repo = filepath.Join(root, "R")
	leaf = filepath.Join(repo, ".claude", "worktrees", "w1")
	target = filepath.Join(root, "T")
	writeFile(t, filepath.Join(target, "w1", "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
	writeFile(t, filepath.Join(target, "w1", "HEAD"), "ref: refs/heads/b1\n", 0o644)
	writeFile(t, filepath.Join(target, "w1", "commondir"), "../..\n", 0o644)
	mkdirForTest(t, filepath.Join(repo, ".git"))
	if err := os.Symlink(target, filepath.Join(repo, ".git", "worktrees")); err != nil {
		t.Fatal(err)
	}
	return repo, leaf, target
}

// wantEntryKept fails the test unless T holds w1 with its three files.
func wantEntryKept(t *testing.T, target string) {
	t.Helper()
	want := []string{"w1", "w1/HEAD", "w1/commondir", "w1/gitdir"}
	if got := pathsBelow(t, target); !slices.Equal(got, want) {
		t.Errorf("T holds %q, want %q", got, want)
	}
}

// TestCreateDeletesNothingBehindALinkedRegistrationsFolder covers the three
// deletes of a create on Linux and macOS. Without the link refusal each one
// deletes T/w1:
//
//   - The step before the add, with no held git directory: the link was
//     opened by its path (openRegistrations with refuseLink).
//   - The rollback delete of a tested registration (removeTestedRegistration).
//   - The rollback delete with no answer of git (createdWorktreeAdminDir).
func TestCreateDeletesNothingBehindALinkedRegistrationsFolder(t *testing.T) {
	t.Run("the step before the add", func(t *testing.T) {
		repo, leaf, target := linkedRegistrations(t)
		dropStaleWorktreeRegistration(repo, leaf, nil)
		wantEntryKept(t, target)
	})
	t.Run("the rollback of a tested registration", func(t *testing.T) {
		repo, _, target := linkedRegistrations(t)
		tested := acceptRegistration(filepath.Join(repo, ".git", "worktrees", "w1"))
		defer tested.release()
		if err := removeTestedRegistration(tested); err == nil {
			t.Error("removeTestedRegistration = nil, want the error of the link")
		}
		wantEntryKept(t, target)
	})
	t.Run("the rollback with no answer of git", func(t *testing.T) {
		repo, leaf, target := linkedRegistrations(t)
		writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "w1")+"\n", 0o644)
		reg := createdWorktreeAdminDir(repo, leaf)
		if reg.resolved != "" {
			t.Errorf("createdWorktreeAdminDir = %+v, want no registration to delete", reg)
		}
		// The error keeps the branch, as on the path of a tested registration.
		want := realDirText(filepath.Join(repo, ".git", "worktrees"))
		if err := removeCreatedRegistration(reg); err == nil || err.Error() != want {
			t.Errorf("removeCreatedRegistration = %v, want %s", err, want)
		}
		wantEntryKept(t, target)
	})
	t.Run("a link put there after the check with no answer of git", func(t *testing.T) {
		// The registrations folder is real at the check. Then it moves to T and
		// a link to it takes its path. The moved folder keeps its identity, so
		// only the refusal of the link stops the delete.
		repo, leaf, target := linkedRegistrations(t)
		dir := filepath.Join(repo, ".git", "worktrees")
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(target, dir); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+filepath.Join(dir, "w1")+"\n", 0o644)
		reg := createdWorktreeAdminDir(repo, leaf)
		if reg.resolved == "" {
			t.Fatalf("createdWorktreeAdminDir = %+v, want the registration", reg)
		}
		if err := os.Rename(dir, target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}
		if err := removeCreatedRegistration(reg); err == nil {
			t.Error("removeCreatedRegistration = nil, want the error of the link")
		}
		wantEntryKept(t, target)
	})
	t.Run("a link put there right before the open", func(t *testing.T) {
		repo, _, target := linkedRegistrations(t)
		dir := filepath.Join(repo, ".git", "worktrees")
		// The registrations folder starts as a real folder with its own w1.
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "w1", "HEAD"), "ref: refs/heads/b1\n", 0o644)
		tested := acceptRegistration(filepath.Join(dir, "w1"))
		defer tested.release()
		old := openRegistrationsRoot
		t.Cleanup(func() { openRegistrationsRoot = old })
		opens := 0
		openRegistrationsRoot = func(name string) (*os.Root, error) {
			opens++
			if err := os.Rename(dir, dir+"-real"); err != nil {
				return nil, err
			}
			if err := os.Symlink(target, dir); err != nil {
				return nil, err
			}
			return old(name)
		}
		if err := removeTestedRegistration(tested); err == nil {
			t.Error("removeTestedRegistration = nil, want the error of the other folder")
		}
		if opens != 1 {
			t.Fatalf("the registrations folder was opened %d times, want 1", opens)
		}
		wantEntryKept(t, target)
		if got := pathsBelow(t, dir+"-real"); !slices.Equal(got, []string{"w1", "w1/HEAD"}) {
			t.Errorf("the real folder holds %q", got)
		}
	})
}

// TestRemoveDeletesNothingBehindALinkedRegistrationsFolder covers the two
// deletes of a remove. Without the link refusal of openRegistrationsFolder
// each one deletes T/w1.
func TestRemoveDeletesNothingBehindALinkedRegistrationsFolder(t *testing.T) {
	t.Run("the entry by its record", func(t *testing.T) {
		repo, leaf, target := linkedRegistrations(t)
		dropWorktreeEntryByPath(filepath.Join(repo, ".git"), newWorktreePathSet(leaf))
		wantEntryKept(t, target)
	})
	t.Run("the verified entry", func(t *testing.T) {
		repo, _, target := linkedRegistrations(t)
		if err := dropWorktreeEntry(filepath.Join(repo, ".git"), "w1"); err == nil {
			t.Error("dropWorktreeEntry = nil, want the error of the link")
		}
		wantEntryKept(t, target)
	})
	t.Run("the lock check reads nothing", func(t *testing.T) {
		repo, leaf, _ := linkedRegistrations(t)
		if _, readable, _ := worktreeLockedByPath(filepath.Join(repo, ".git"), newWorktreePathSet(leaf)); readable {
			t.Error("worktreeLockedByPath read the folder behind the link")
		}
	})
}
