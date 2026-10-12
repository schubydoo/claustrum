//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The guard tests of the Linux and macOS steps that open the registrations
// folder through the held git directory (acceptedGitDir.registrationsRoot),
// and of the `worktrees` folder that a create makes. Each test puts a symbolic
// link to the folder T at the path of the git directory after the create took
// its hold. No row of 5fd08069 has a swap at these steps. The rule is that of
// the swap cells of val-s3p2b2-9-linux2 and val-s3p2b2-9-macos (Linux and
// macOS VMs): T keeps every entry as it was.

// heldWorktrees is the recorder of newHeldFixture set to the folder
// `worktrees` of the held git directory: the steps of this file remove through
// a root at that folder.
func heldWorktrees(t *testing.T, f heldFixture) *[]string {
	t.Helper()
	fi, err := os.Stat(filepath.Join(f.gitDir, "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	return recordHeldRemovals(t, fi)
}

// TestStaleStepThroughTheHeldGitDir: the step before the leaf lists, reads and
// removes through the held folder. The held folder and T each hold one old
// registration whose record names the missing leaf. The removal is a recorder.
// With a root that is opened by the path the recorder gets the folder of T.
func TestStaleStepThroughTheHeldGitDir(t *testing.T) {
	f := newHeldFixture(t)
	removals := heldWorktrees(t, f)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "gone")
	record := filepath.Join(leaf, ".git") + "\n"
	mustWrite(t, filepath.Join(f.gitDir, "worktrees", "old", "gitdir"), record)
	mustWrite(t, filepath.Join(f.other, "worktrees", "old", "gitdir"), record)
	f.swap(t)
	dropStaleWorktreeRegistration(f.repo, leaf, f.u.accepted)
	if got := strings.Join(*removals, ","); got != "old held" {
		t.Errorf("the step asked to remove %q, want the old registration of the held folder", got)
	}
}

// TestTestedRegistrationThroughTheHeldGitDir: the rollback delete and the
// placement of the index of an accepted registration go through the held
// folder. Both have the identity test of D24, so a step by the path removes
// and places nothing in T. It then leaves the registration of the held folder
// too: the recorder gets no call, and the held folder gets no index.
func TestTestedRegistrationThroughTheHeldGitDir(t *testing.T) {
	f := newHeldFixture(t)
	removals := heldWorktrees(t, f)
	tested := acceptRegistration(filepath.Join(f.gitDir, "worktrees", "wr"))
	tested.git = f.u.accepted
	t.Cleanup(tested.release)
	src := filepath.Join(resolveTestRoot(t, t.TempDir()), "index")
	mustWrite(t, src, "the index of the checkout\n")
	f.swap(t)
	if err := installTestedWorktreeIndex(src, tested); err != nil {
		t.Errorf("the placement: %v", err)
	}
	if got := mustRead(t, filepath.Join(f.realDir, "worktrees", "wr", "index")); got != "the index of the checkout\n" {
		t.Errorf("the index of the held folder = %q, want the bytes of the checkout", got)
	}
	if err := removeTestedRegistration(tested); err != nil {
		t.Errorf("the delete: %v", err)
	}
	if got := strings.Join(*removals, ","); got != "wr held" {
		t.Errorf("the rollback asked to remove %q, want the registration of the held folder", got)
	}
	f.otherUntouched(t)
}

// TestWorktreesFolderThroughTheHeldGitDir: the folder `worktrees` that a
// create makes before its `worktree add` goes into the held folder. With a
// root that is opened by the path T gets the folder.
func TestWorktreesFolderThroughTheHeldGitDir(t *testing.T) {
	f := newHeldFixture(t)
	other := filepath.Join(resolveTestRoot(t, t.TempDir()), "empty")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.gitDir, f.realDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.realDir, "worktrees"), filepath.Join(f.realDir, "worktrees-kept")); err != nil {
		t.Fatal(err)
	}
	linkAt(t, f.gitDir, other)
	f.u.makeWorktreesFolder()
	if ents, err := os.ReadDir(other); err != nil || len(ents) != 0 {
		t.Errorf("the other folder holds %d entries (%v), want none", len(ents), err)
	}
	if fi, err := os.Lstat(filepath.Join(f.realDir, "worktrees")); err != nil || !fi.IsDir() {
		t.Errorf("the folder worktrees of the held folder: %v, want a folder", err)
	}
}

// TestRegistrationsRootOfALinkedWorktreesFolder: a `worktrees` entry of the
// held git directory that is a symbolic link is opened by its path, as before.
// 89cb6289 creates with such a link (rows D-1 and D-3 to D-5, Linux VM).
func TestRegistrationsRootOfALinkedWorktreesFolder(t *testing.T) {
	f := newHeldFixture(t)
	worktrees := filepath.Join(f.gitDir, "worktrees")
	moved := filepath.Join(filepath.Dir(f.repo), "WTREG")
	if err := os.Rename(worktrees, moved); err != nil {
		t.Fatal(err)
	}
	linkAt(t, worktrees, moved)
	root, err := f.u.accepted.registrationsRoot(worktrees)
	if err != nil {
		t.Fatalf("the open: %v", err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat("wr"); err != nil {
		t.Errorf("the registration through the link: %v", err)
	}
}
