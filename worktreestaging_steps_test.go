package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The steps of the Windows staging move (addStage.move), on every system: the
// staged add runs here directly. The rows are those of round task087-01
// (Windows VM). The Windows frames of the create are in
// worktreestaging_steps_windows_test.go.

// stepFixture is a fixture with a held git directory, so that the step hooks
// of the create run (atStep).
type stepFixture struct {
	repoFolderFixture
	u gitUse
}

func newStepFixture(t *testing.T) stepFixture {
	t.Helper()
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	setGitDirMustBeReal(t, true)
	u := heldUse(t, f.gitDir, f.useOf(t, f.repo))
	return stepFixture{repoFolderFixture: f, u: u}
}

// stagedWorktrees is the folder `worktrees` of the one `.s<n>` of u.
func stagedWorktrees(t *testing.T, u gitUse) string {
	t.Helper()
	names := addFolders(t, u.folder)
	if len(names) != 1 {
		t.Fatalf("the folder of the daemon holds %q, want one .s<n>", names)
	}
	return filepath.Join(u.folder, names[0], worktreesSubdir)
}

// nothingMade asserts that the git directory has no staging entry and no
// registration w1.
func (f stepFixture) nothingMade(t *testing.T) {
	t.Helper()
	if got := stagingEntries(t, f.gitDir); len(got) != 0 {
		t.Errorf("the staging entries of the git directory = %q, want none", got)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees", "w1")); !os.IsNotExist(err) {
		t.Errorf("the registration w1: %v, want none", err)
	}
}

// incompleteText is the text of an addIncomplete error, or "".
func incompleteText(err error) string {
	var incomplete *addIncomplete
	if errors.As(err, &incomplete) {
		return incomplete.text
	}
	return ""
}

// TestStagedMoveWithTwoNewNames pins step 1 (cell S1): a second new name in
// `.s<n>\worktrees` fails the move before anything is made in the git
// directory. Without the count the move takes no name and logs a line.
func TestStagedMoveWithTwoNewNames(t *testing.T) {
	f := newStepFixture(t)
	atStep(t, "added", func() {
		mustWrite(t, filepath.Join(stagedWorktrees(t, f.u), "w9", "HEAD"), "ref: refs/heads/main\n")
	})
	_, _, err := stagedAdd(t, f.repoFolderFixture, f.u)
	if got, want := incompleteText(err), "git registered 2 worktrees where one was expected; try again"; got != want {
		t.Errorf("the add = %v, want the text %q", err, want)
	}
	f.nothingMade(t)
}

// TestStagedMoveWithALeafOfAnotherEntry pins step 2 (cell S2): a `.git` file of
// the leaf that names another entry of `.s<n>` refuses the move with
// otherEntryRefusal, before anything is made in the git directory. Without the
// test the move goes on and the file keeps its text.
func TestStagedMoveWithALeafOfAnotherEntry(t *testing.T) {
	f := newStepFixture(t)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	atStep(t, "added", func() {
		mustWrite(t, filepath.Join(leaf, ".git"), "gitdir: "+filepath.ToSlash(filepath.Join(stagedWorktrees(t, f.u), "w9"))+"\n")
	})
	_, _, err := stagedAdd(t, f.repoFolderFixture, f.u)
	var refused *addRefused
	if !errors.As(err, &refused) || refused.text != otherEntryRefusal(leaf) {
		t.Errorf("the add = %v, want the refusal %s", err, otherEntryRefusal(leaf))
	}
	f.nothingMade(t)
}

// TestStagedMoveCopiesTheTree pins step 3 (cell S3): a file in a sub folder of
// the staged registration is in the moved registration. The move records the
// moved entry. Without the copy of the tree the sub folder is empty.
func TestStagedMoveCopiesTheTree(t *testing.T) {
	f := newStepFixture(t)
	atStep(t, "added", func() {
		mustWrite(t, filepath.Join(stagedWorktrees(t, f.u), "w1", "sub", "f.txt"), "x\n")
	})
	if _, stderr, err := stagedAdd(t, f.repoFolderFixture, f.u); err != nil {
		t.Fatalf("the add failed: %v, %s", err, stderr)
	}
	if got := mustRead(t, filepath.Join(f.gitDir, "worktrees", "w1", "sub", "f.txt")); got != "x\n" {
		t.Errorf("sub/f.txt of the registration = %q", got)
	}
	if got := f.u.accepted.staged; got.name != "w1" || got.info == nil {
		t.Errorf("the moved entry = %+v, want w1 with its identity", got)
	}
	if got := stagingEntries(t, f.gitDir); len(got) != 0 {
		t.Errorf("the staging entries of the git directory = %q, want none", got)
	}
}

// TestStagedMoveFailsAtALink pins the failure of step 3 (cell S4): a link that
// leads out of the staged registration fails the move with the error of its
// open, `openat j: path escapes from parent`. The staging folder goes, and the
// target of the link stays. Without the open of each entry the link is passed
// over and the move succeeds.
func TestStagedMoveFailsAtALink(t *testing.T) {
	if os.Symlink(t.TempDir(), filepath.Join(t.TempDir(), "probe")) != nil {
		t.Skip("this host makes no symbolic link for the test")
	}
	f := newStepFixture(t)
	tgt := t.TempDir()
	mustWrite(t, filepath.Join(tgt, "keep.txt"), "keep\n")
	atStep(t, "added", func() {
		if err := os.Symlink(tgt, filepath.Join(stagedWorktrees(t, f.u), "w1", "j")); err != nil {
			t.Error(err)
		}
	})
	_, _, err := stagedAdd(t, f.repoFolderFixture, f.u)
	if got, want := incompleteText(err), "openat j: path escapes from parent"; got != want {
		t.Errorf("the add = %v, want the text %q", err, want)
	}
	f.nothingMade(t)
	if got := mustRead(t, filepath.Join(tgt, "keep.txt")); got != "keep\n" {
		t.Errorf("the file behind the link = %q", got)
	}
}

// TestStagedMoveWithAHundredNames pins the failure of step 4 (cell S6v). With
// w1 and w11 to w199 in the git directory the move fails with the text of
// 5fd08069. The 100 entries stay. Without registrationTarget the move
// refuses the entry w1 with a log line.
func TestStagedMoveWithAHundredNames(t *testing.T) {
	f := newStepFixture(t)
	names := []string{"w1"}
	for i := 1; i < 100; i++ {
		names = append(names, "w1"+strconv.Itoa(i))
	}
	atStep(t, "added", func() {
		for _, n := range names {
			mustWrite(t, filepath.Join(f.gitDir, "worktrees", n, "f.txt"), "x\n")
		}
	})
	_, _, err := stagedAdd(t, f.repoFolderFixture, f.u)
	want := `the repository already has a hundred worktrees called "w1"; remove some (git worktree prune, git worktree remove)`
	if got := incompleteText(err); got != want {
		t.Errorf("the add = %v, want the text %q", err, want)
	}
	if got := stagingEntries(t, f.gitDir); len(got) != 0 {
		t.Errorf("the staging entries of the git directory = %q, want none", got)
	}
	for _, n := range names {
		if got := listTree(t, filepath.Join(f.gitDir, "worktrees", n)); len(got) != 1 || got[0] != "f.txt = x\n" {
			t.Errorf("the entry %s holds %q", n, got)
		}
	}
}

// TestStagedRecordRefusal pins the record test after the move (cells S14 and
// S14a). An absolute record of the leaf passes. A relative record that names
// the leaf from `.s<n>` names another path from the moved registration, and a
// record that cannot be read is refused too. Without the test every record
// passes.
func TestStagedRecordRefusal(t *testing.T) {
	f := newStepFixture(t)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	reg := filepath.Join(f.gitDir, "worktrees", "w9")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	f.u.accepted.staged = stagedEntry{name: "w9"}
	if got := stagedRecordRefusal(f.u.accepted, leaf); got != adminRecordRefusal(leaf) {
		t.Errorf("with no record = %q, want the refusal", got)
	}
	mustWrite(t, filepath.Join(reg, "gitdir"), filepath.ToSlash(filepath.Join(leaf, ".git"))+"\n")
	if got := stagedRecordRefusal(f.u.accepted, leaf); got != "" {
		t.Errorf("with the absolute record = %q, want none", got)
	}
	mustWrite(t, filepath.Join(reg, "gitdir"), "../../x/.claude/worktrees/w1/.git\n")
	if got := stagedRecordRefusal(f.u.accepted, leaf); got != adminRecordRefusal(leaf) {
		t.Errorf("with a relative record of another path = %q, want the refusal", got)
	}
}

// openTestRoot opens a root at dir until the test ends.
func openTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// TestReadRegistrationFile pins the read of one file of the staged
// registration: a file over registrationFileMaxBytes and a file that is gone
// fail with their texts, and the bytes of a good file come back.
func TestReadRegistrationFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/w1\n")
	if err := os.WriteFile(filepath.Join(dir, "big"), make([]byte, registrationFileMaxBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	root := openTestRoot(t, dir)
	if b, err := readRegistrationFile(root, "HEAD"); err != nil || string(b) != "ref: refs/heads/w1\n" {
		t.Errorf("HEAD = %q, %v", b, err)
	}
	if _, err := readRegistrationFile(root, "big"); err == nil || err.Error() != "the file big of the new registration is too large" {
		t.Errorf("big = %v, want the size refusal", err)
	}
	if _, err := readRegistrationFile(root, "gone"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("gone = %v, want a missing file", err)
	}
}

// TestCopyRegistrationTreeStopsAtAnEntryThatIsThere: the copy makes only new
// folders and files. A folder or a file that is already at its place in the
// staging folder fails the copy, and that entry keeps its bytes.
func TestCopyRegistrationTreeStopsAtAnEntryThatIsThere(t *testing.T) {
	for _, row := range []struct {
		name  string
		stage func(t *testing.T, dst string)
		kept  string
	}{
		{"a folder at the top", func(t *testing.T, dst string) { mustWrite(t, filepath.Join(dst, "s", "sub", "k"), "k\n") }, filepath.Join("s", "sub", "k")},
		{"a folder below", func(t *testing.T, dst string) { mustWrite(t, filepath.Join(dst, "s", "sub", "deep", "k"), "k\n") }, filepath.Join("s", "sub", "deep", "k")},
		{"a file", func(t *testing.T, dst string) { mustWrite(t, filepath.Join(dst, "s", "HEAD"), "k\n") }, filepath.Join("s", "HEAD")},
	} {
		t.Run(row.name, func(t *testing.T) {
			src, dst := t.TempDir(), t.TempDir()
			mustWrite(t, filepath.Join(src, "HEAD"), "ref: refs/heads/w1\n")
			mustWrite(t, filepath.Join(src, "sub", "deep", "f"), "x\n")
			row.stage(t, dst)
			if err := copyRegistrationTree(openTestRoot(t, src), openTestRoot(t, dst), "s", ""); !errors.Is(err, os.ErrExist) {
				t.Errorf("the copy = %v, want an entry that exists", err)
			}
			if got := mustRead(t, filepath.Join(dst, row.kept)); got != "k\n" {
				t.Errorf("%s = %q, want it kept", row.kept, got)
			}
		})
	}
	t.Run("a source that cannot be listed", func(t *testing.T) {
		src, dst := t.TempDir(), t.TempDir()
		if err := copyRegistrationTree(openTestRoot(t, src), openTestRoot(t, dst), "s", "gone"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the copy = %v, want a missing folder", err)
		}
	})
}

// TestLeafGitFileGuards pins the guards of the read and the rewrite of the
// `.git` file of the leaf: no checkpoint, a leaf that is gone, a leaf that is
// another folder than the checkpoint, and a `.git` that is no regular file. In
// each case the rewrite changes nothing.
func TestLeafGitFileGuards(t *testing.T) {
	base := t.TempDir()
	leaf := filepath.Join(base, "w1")
	mustWrite(t, filepath.Join(leaf, ".git"), "gitdir: old\n")
	cp := checkpointCreatedWorktree(leaf)
	t.Cleanup(cp.release)
	other := filepath.Join(base, "w2")
	mustWrite(t, filepath.Join(other, ".git"), "gitdir: other\n")
	dirGit := filepath.Join(base, "w3")
	if err := os.MkdirAll(filepath.Join(dirGit, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cp3 := checkpointCreatedWorktree(dirGit)
	t.Cleanup(cp3.release)
	for _, row := range []struct {
		name, leaf string
		cp         worktreeCheckpoint
		read       string
		write      string
	}{
		{"no checkpoint", leaf, worktreeCheckpoint{}, "the create holds no checkpoint of the new worktree", "the create holds no checkpoint of the new worktree"},
		{"another folder", other, cp, "the new worktree is no longer the folder of the create", "the new worktree is no longer the folder of the create"},
		{"a folder at .git", dirGit, cp3, "the .git file of the new worktree cannot be read", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			if _, err := readLeafGitFile(row.leaf, row.cp); err == nil || err.Error() != row.read {
				t.Errorf("the read = %v, want %q", err, row.read)
			}
			if row.write == "" {
				return
			}
			if err := rewriteLeafGitFile(row.leaf, row.cp, "gitdir: new\n"); err == nil || err.Error() != row.write {
				t.Errorf("the rewrite = %v, want %q", err, row.write)
			}
		})
	}
	if got := mustRead(t, filepath.Join(other, ".git")); got != "gitdir: other\n" {
		t.Errorf("the .git file of the other folder = %q", got)
	}
	gone := filepath.Join(base, "gone")
	if _, err := readLeafGitFile(gone, cp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the read of a gone leaf = %v", err)
	}
	if err := rewriteLeafGitFile(gone, cp, "gitdir: new\n"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the rewrite of a gone leaf = %v", err)
	}
	if got := (&addRefused{text: "refused"}).Error(); got != "refused" {
		t.Errorf("addRefused.Error = %q", got)
	}
}

// TestRemoveOwnStagingKeepsAFolderThatHoldsHome: the staging remove runs
// wipesHomeDir first. A staging folder that holds the home folder stays, and
// one line is logged.
func TestRemoveOwnStagingKeepsAFolderThatHoldsHome(t *testing.T) {
	gitDir := t.TempDir()
	name := stagingNamePrefix + "0123456789abcdef"
	home := filepath.Join(gitDir, name, "home")
	mustWrite(t, filepath.Join(home, "f.txt"), "home\n")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	logs := captureLogBuf(t)
	st := &addStage{u: gitUse{gitDir: gitDir}, git: openTestRoot(t, gitDir)}
	st.removeOwnStaging(name)
	if got := mustRead(t, filepath.Join(home, "f.txt")); got != "home\n" {
		t.Errorf("the file in home = %q", got)
	}
	if !strings.Contains(logs.String(), "refusing to remove the staging folder "+name) {
		t.Errorf("no log line for the refusal: %q", logs.String())
	}
}
