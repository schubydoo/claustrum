package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The guard tests of the steps that act through the held git directory
// (gitdirheld.go). Each test holds the git directory of a repository, moves it
// to `<git directory>-real` and puts a link to another folder T at its path:
// a symbolic link on Linux and macOS, and a junction on Windows (linkAt). T
// has a registration `wr` of its own, whose record names the same worktree, so
// a step that goes through the path finds a match in T.
//
// No row of 5fd08069 has a swap at these steps. The rule is that of cells X1c
// and X1p of val-s3p2b2-7-windows (Windows VM): nothing in T goes, and T gets
// nothing. Every removal is a recorder, and each folder is below t.TempDir.

// heldFixture is a repository with the linked worktree `wr`, a create that
// holds its git directory, and the folder T.
type heldFixture struct {
	repoFolderFixture
	u gitUse
	// realDir is the path of the held folder after swap. otherReg is the
	// registration `wr` of T.
	realDir, other, otherReg string
	removals                 *[]string
}

func newHeldFixture(t *testing.T) heldFixture {
	t.Helper()
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	setGitDirMustBeReal(t, true)
	u := f.useOf(t, f.repo)
	u.accepted = acceptGitDir(f.gitDir, gitDirIdentity(f.gitDir))
	if u.accepted == nil {
		t.Fatal("the git directory of the fixture is not accepted")
	}
	t.Cleanup(u.accepted.release)
	other := filepath.Join(resolveTestRoot(t, t.TempDir()), "T")
	otherReg := filepath.Join(other, "worktrees", "wr")
	mustWrite(t, filepath.Join(otherReg, "gitdir"), mustRead(t, filepath.Join(f.gitDir, "worktrees", "wr", "gitdir")))
	mustWrite(t, filepath.Join(otherReg, "index"), "the index of the other folder\n")
	mustWrite(t, filepath.Join(otherReg, "marker.txt"), "marker of the other folder\n")
	return heldFixture{repoFolderFixture: f, u: u, realDir: f.gitDir + "-real", other: other, otherReg: otherReg,
		removals: recordHeldRemovals(t, u.accepted.info)}
}

// recordHeldRemovals replaces the removal of the held steps with a recorder
// that removes nothing. It returns the calls as `<name> held`, or `<name>
// other` for a root that is not the folder with the identity held.
func recordHeldRemovals(t *testing.T, held os.FileInfo) *[]string {
	t.Helper()
	var calls []string
	old := removeHeldEntry
	t.Cleanup(func() { removeHeldEntry = old })
	removeHeldEntry = func(root *os.Root, name string) error {
		where := "other"
		if fi, err := root.Stat("."); err == nil && os.SameFile(fi, held) {
			where = "held"
		}
		calls = append(calls, filepath.ToSlash(name)+" "+where)
		return nil
	}
	return &calls
}

// swap moves the git directory away and puts the link to T at its path.
func (f heldFixture) swap(t *testing.T) {
	t.Helper()
	if err := os.Rename(f.gitDir, f.realDir); err != nil {
		t.Fatal(err)
	}
	linkAt(t, f.gitDir, f.other)
	if f.u.accepted.stands() {
		t.Fatal("the path still names the held folder after the swap")
	}
}

// otherUntouched fails the test when the registration of T has another entry
// or another text than newHeldFixture gave it.
func (f heldFixture) otherUntouched(t *testing.T) {
	t.Helper()
	ents, err := os.ReadDir(f.otherReg)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if got := strings.Join(names, " "); got != "gitdir index marker.txt" {
		t.Errorf("the registration of the other folder holds %q, want its three files", got)
	}
	if got := mustRead(t, filepath.Join(f.otherReg, "index")); got != "the index of the other folder\n" {
		t.Errorf("the index of the other folder = %q", got)
	}
	if ents, err := os.ReadDir(filepath.Join(f.other, "worktrees")); err != nil || len(ents) != 1 {
		t.Errorf("the worktrees folder of the other folder holds %d entries (%v), want 1", len(ents), err)
	}
}

// TestHeldRollbackRegistration is the guard test of the rollback delete of the
// registration. After the swap the delete is asked of the held folder, by the
// name of the registration, and T keeps its registration. With a root that is
// opened by the path the recorder gets the root of T. The second part pins the
// tests before the delete: a registration outside `worktrees` of the held
// folder, a registration of another worktree and a link at the name stay.
func TestHeldRollbackRegistration(t *testing.T) {
	t.Run("after a swap", func(t *testing.T) {
		f := newHeldFixture(t)
		f.swap(t)
		f.u.accepted.removeCreatedRegistration(filepath.Join(f.gitDir, "worktrees", "wr"), f.wt)
		if got := strings.Join(*f.removals, ","); got != "worktrees/wr held" {
			t.Errorf("the rollback asked to remove %q, want the registration of the held folder", got)
		}
		f.otherUntouched(t)
	})
	t.Run("what stays", func(t *testing.T) {
		f := newHeldFixture(t)
		a := f.u.accepted
		a.removeCreatedRegistration(filepath.Join(f.other, "worktrees", "wr"), f.wt)
		a.removeCreatedRegistration(filepath.Join(f.gitDir, "wr"), f.wt)
		a.removeCreatedRegistration(filepath.Join(f.gitDir, "worktrees", "wr"), filepath.Join(f.repo, "another"))
		a.removeCreatedRegistration("", f.wt)
		linkAt(t, filepath.Join(f.gitDir, "worktrees", "ln"), f.otherReg)
		a.removeCreatedRegistration(filepath.Join(f.gitDir, "worktrees", "ln"), f.wt)
		if len(*f.removals) != 0 {
			t.Errorf("the rollback asked to remove %q, want nothing", *f.removals)
		}
		a.removeCreatedRegistration(filepath.Join(f.gitDir, "worktrees", "wr"), f.wt)
		if got := strings.Join(*f.removals, ","); got != "worktrees/wr held" {
			t.Errorf("the rollback asked to remove %q, want the registration", got)
		}
	})
}

// TestRollbackOfAHeldCreate: the rollback of a create that holds its git
// directory asks the held folder for the delete of the registration that the
// `.git` file of the leaf names. Without the arm of the held folder in
// undoFailedCheckout the recorder gets no call.
func TestRollbackOfAHeldCreate(t *testing.T) {
	f := newHeldFixture(t)
	leaf, stderr, err := stagedAdd(t, f.repoFolderFixture, f.u)
	if err != nil {
		t.Fatalf("the add failed: %v, %s", err, stderr)
	}
	cp := checkpointCreatedWorktree(leaf)
	t.Cleanup(cp.release)
	if text, _ := undoFailedCheckout(f.u, f.repo, leaf, "", cp, testedRegistration{}); text != "" {
		t.Errorf("the undo text = %q, want none", text)
	}
	if got := strings.Join(*f.removals, ","); got != "worktrees/w1 held" {
		t.Errorf("the rollback asked to remove %q, want the registration of the new worktree", got)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
}

// TestHeldIndexPlacement is the guard test of the index placement. After the
// swap the index goes into the registration of the held folder, and the index
// of T keeps its bytes. No temporary file stays. With a placement by the path
// the index of T gets the bytes of the checkout.
func TestHeldIndexPlacement(t *testing.T) {
	f := newHeldFixture(t)
	src := filepath.Join(resolveTestRoot(t, t.TempDir()), "index")
	mustWrite(t, src, "the index of the checkout\n")
	f.swap(t)
	f.u.accepted.installIndex(src, filepath.Join(f.gitDir, "worktrees", "wr"))
	f.otherUntouched(t)
	held := filepath.Join(f.realDir, "worktrees", "wr")
	if got := mustRead(t, filepath.Join(held, "index")); got != "the index of the checkout\n" {
		t.Errorf("the index of the held folder = %q, want the bytes of the checkout", got)
	}
	ents, err := os.ReadDir(held)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(ents, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), "index.tmp-") }) {
		t.Errorf("a temporary file stays in %s", held)
	}
	if got := mustRead(t, src); got != "the index of the checkout\n" {
		t.Errorf("the index of the checkout = %q, want it kept", got)
	}
	// A registration outside the held folder, and a link at the name, get none.
	f.u.accepted.installIndex(src, filepath.Join(f.other, "worktrees", "wr"))
	linkAt(t, filepath.Join(f.realDir, "worktrees", "ln"), f.otherReg)
	f.u.accepted.installIndex(src, filepath.Join(f.gitDir, "worktrees", "ln"))
	f.otherUntouched(t)
}

// TestHeldStaleRegistration is the guard test of the step before the leaf.
// The held folder and T each hold a registration `old` whose record names the
// missing leaf. After the swap the delete is asked of the held folder, and T
// keeps its entry. With a listing or a delete by the path the recorder gets
// the root of T, or no call.
func TestHeldStaleRegistration(t *testing.T) {
	f := newHeldFixture(t)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "gone")
	record := filepath.Join(leaf, ".git") + "\n"
	mustWrite(t, filepath.Join(f.gitDir, "worktrees", "old", "gitdir"), record)
	otherOld := filepath.Join(f.other, "worktrees", "old-other")
	mustWrite(t, filepath.Join(otherOld, "gitdir"), record)
	f.swap(t)
	f.u.accepted.dropStaleRegistration(leaf)
	if got := strings.Join(*f.removals, ","); got != "worktrees/old held" {
		t.Errorf("the step asked to remove %q, want the old registration of the held folder", got)
	}
	if got := mustRead(t, filepath.Join(otherOld, "gitdir")); got != record {
		t.Errorf("the record of the other folder = %q", got)
	}
	// A leaf that no record names removes nothing.
	*f.removals = nil
	f.u.accepted.dropStaleRegistration(filepath.Join(f.repo, ".claude", "worktrees", "none"))
	if len(*f.removals) != 0 {
		t.Errorf("the step asked to remove %q, want nothing", *f.removals)
	}
}

// TestHeldWorktreeConfig is the guard test of the `config.worktree` file of a
// new registration. After the swap the file goes into the registration of the
// held folder, and T gets no file. With a root that is opened by the path T
// gets the file.
func TestHeldWorktreeConfig(t *testing.T) {
	f := newHeldFixture(t)
	f.swap(t)
	text := []byte("[core]\n\teol = lf\n")
	if err := writeWorktreeConfig(f.u, filepath.Join(f.gitDir, "worktrees", "wr"), f.wt, testedRegistration{}, text); err != nil {
		t.Errorf("the write: %v", err)
	}
	f.otherUntouched(t)
	if got := mustRead(t, filepath.Join(f.realDir, "worktrees", "wr", worktreeConfigName)); got != string(text) {
		t.Errorf("the file of the held folder = %q", got)
	}
}
