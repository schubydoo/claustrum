package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tests of the held git directory of a create (gitdiraccept.go). The rows
// are cells X1c and X1p and table W3 of val-s3p2b2-7-windows (Windows VM).
// linkAt makes the link of a swap: a symbolic link on Linux and macOS, and a
// junction on Windows.

// setGitDirMustBeReal sets the switch of the steps for one test.
func setGitDirMustBeReal(t *testing.T, on bool) {
	t.Helper()
	old := gitDirMustBeReal
	t.Cleanup(func() { gitDirMustBeReal = old })
	gitDirMustBeReal = on
}

// swapTarget is the folder T of the rows: a folder that is no git directory,
// with an old staging entry, its marker and one more file.
type swapTarget struct {
	dir, marker string
}

func newSwapTarget(t *testing.T) swapTarget {
	t.Helper()
	dir := filepath.Join(resolveTestRoot(t, t.TempDir()), "T")
	entry := filepath.Join(dir, ".claude-new-worktree-x")
	s := swapTarget{dir: dir, marker: filepath.Join(entry, "marker.txt")}
	mustWrite(t, s.marker, "marker of the old entry\n")
	mustWrite(t, filepath.Join(dir, "t-own.txt"), "a file of the other folder\n")
	at := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(entry, at, at); err != nil {
		t.Fatal(err)
	}
	return s
}

// untouched fails the test when the folder has another entry than the two that
// newSwapTarget made, or another marker text.
func (s swapTarget) untouched(t *testing.T) {
	t.Helper()
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != ".claude-new-worktree-x t-own.txt" {
		t.Errorf("the other folder holds %q, want its two entries", names)
	}
	if text := mustRead(t, s.marker); text != "marker of the old entry\n" {
		t.Errorf("the marker of the other folder = %q", text)
	}
}

// swapAt puts a link to target at gitDir when the create reaches step, one
// time. The git directory itself goes to `<gitDir>-real`.
func swapAt(t *testing.T, step, gitDir, target string) {
	t.Helper()
	old := createStepHook
	t.Cleanup(func() { createStepHook = old })
	done := false
	createStepHook = func(at string) {
		if at != step || done {
			return
		}
		done = true
		if err := os.Rename(gitDir, gitDir+"-real"); err != nil {
			t.Errorf("the rename of the git directory: %v", err)
			return
		}
		linkAt(t, gitDir, target)
	}
}

// atStep runs do when the create reaches step, one time.
func atStep(t *testing.T, step string, do func()) {
	t.Helper()
	old := createStepHook
	t.Cleanup(func() { createStepHook = old })
	done := false
	createStepHook = func(at string) {
		if at == step && !done {
			done = true
			do()
		}
	}
}

// TestAcceptGitDir: the create holds a real directory, and the path stands as
// long as it names that folder. A link at the path and another real directory
// at the path do not stand, and neither is accepted with the identity of the
// first folder. Without the identity compare in stands the other real
// directory stands.
func TestAcceptGitDir(t *testing.T) {
	base := resolveTestRoot(t, t.TempDir())
	dir := filepath.Join(base, "g")
	other := filepath.Join(base, "other")
	for _, p := range []string{dir, other} {
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	before := gitDirIdentity(dir)
	a := acceptGitDir(dir, before)
	if a == nil {
		t.Fatal("a real directory is not accepted")
	}
	defer a.release()
	if !a.stands() {
		t.Error("the folder does not stand")
	}
	if acceptGitDir(dir, nil) != nil {
		t.Error("a folder with no identity before the gate is accepted")
	}
	if err := os.Rename(dir, dir+"-real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, dir); err != nil {
		t.Fatal(err)
	}
	if a.stands() {
		t.Error("another real directory at the path stands")
	}
	if got := acceptGitDir(dir, before); got != nil {
		got.release()
		t.Error("another real directory is accepted with the first identity")
	}
	if err := os.Rename(dir, other); err != nil {
		t.Fatal(err)
	}
	linkAt(t, dir, other)
	if a.stands() {
		t.Error("a link at the path stands")
	}
	if gitDirIdentity(dir) != nil {
		t.Error("a link has the identity of a real directory")
	}
	if got := acceptGitDir(dir, before); got != nil {
		got.release()
		t.Error("a link is accepted")
	}
	file := filepath.Join(base, "file")
	mustWrite(t, file, "x\n")
	if gitDirIdentity(file) != nil {
		t.Error("a file has the identity of a real directory")
	}
}

// TestGitDirLinkDetail: a git directory that is a link is refused with the
// text of its config, where the switch is set. On 5fd08069 the text is `…
// "C:\\o\\w\\repo\\.git\\config": is, or is reached through, a link that
// Claude does not follow here; replace the link with what it points to` (cells
// X1c-01 to X1c-05). The read of the config gives it before any other test.
// Without the test in readRepoConfig the read gives no refusal.
func TestGitDirLinkDetail(t *testing.T) {
	f := newRefusalFixture(t)
	link := filepath.Join(resolveTestRoot(t, t.TempDir()), "linked")
	linkAt(t, link, f.gitDir)
	want := quoteOperand(filepath.Join(link, "config")) +
		": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to"

	setGitDirMustBeReal(t, false)
	if got := gitDirLinkDetail(link); got != "" {
		t.Errorf("with the switch off the detail = %q, want none", got)
	}
	gitDirMustBeReal = true
	if got := gitDirLinkDetail(link); got != want {
		t.Errorf("the detail = %q\nwant %q", got, want)
	}
	if got := gitDirLinkDetail(f.gitDir); got != "" {
		t.Errorf("the detail of a real directory = %q, want none", got)
	}
	var log repoReadLog
	if _, got := readRepoConfig(context.Background(), link, &log); got != want {
		t.Errorf("the read of the config = %q\nwant %q", got, want)
	}
}

// TestKeptReadAndALinkAtTheGitDir: a link at the path of the git directory is
// refused also after a request kept the read of its config. The link leads to
// the folder that was read, so each file has the state of the kept read.
// Without the link test before the kept read the gate gives no refusal. A
// junction that is put at `.git` between two requests is not measured on
// 5fd08069: the text is that of cells X1c-01 to X1c-05 of val-s3p2b2-7-windows
// (Windows VM).
func TestKeptReadAndALinkAtTheGitDir(t *testing.T) {
	f := newRefusalFixture(t)
	setGitDirMustBeReal(t, true)
	trust := gitDirTrust{verdict: gitDirTrusted, pinCommonDir: f.gitDir}
	if msg, v := repoConfigGate(trust, false); msg != "" || v == nil {
		t.Fatalf("the first gate = %q, want an accepted read", msg)
	}
	if err := os.Rename(f.gitDir, f.gitDir+"-real"); err != nil {
		t.Fatal(err)
	}
	linkAt(t, f.gitDir, f.gitDir+"-real")
	want := "this repository's git config cannot be used: " + quoteOperand(filepath.Join(f.gitDir, "config")) +
		": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to"
	if msg, _ := repoConfigGate(trust, false); msg != want {
		t.Errorf("the gate with a link = %q\nwant %q", msg, want)
	}
	// The real directory at the path again answers as before.
	if err := os.Remove(f.gitDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.gitDir+"-real", f.gitDir); err != nil {
		t.Fatal(err)
	}
	if msg, v := repoConfigGate(trust, false); msg != "" || v == nil {
		t.Errorf("the gate after the link went = %q, want an accepted read", msg)
	}
}

// stagedSwapFixture is a repository whose create holds its git directory, with
// the folder T, an old staging entry in the real git directory, and the
// removal of the cleanup replaced by a recorder that removes nothing.
type stagedSwapFixture struct {
	repoFolderFixture
	u        gitUse
	target   swapTarget
	removals *[]string
}

func newStagedSwapFixture(t *testing.T) stagedSwapFixture {
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
	plantStagingEntry(t, f.gitDir, ".claude-new-worktree-real", 2*time.Hour)
	return stagedSwapFixture{repoFolderFixture: f, u: u, target: newSwapTarget(t), removals: recordStagingRemovals(t)}
}

// TestStagedAddWithASwappedGitDir is the guard test of the cleanup and of the
// move. A link to another folder T is put at the path of the git directory at
// one step of the staged add. T holds an old staging entry. The removal is a
// recorder, so the test removes nothing with the guard or without it.
//
// The rows are those of 5fd08069 on a Windows VM: nothing in T goes, T gets
// nothing, and the create is refused with the text of the step (cells X1c-09,
// X1p-09, X1p-07, X1c-10 and X1p-10).
//
// Without the test before the folder of the add, git runs and the reply is its
// error. With a root that is opened by the path, T gets a folder `worktrees`.
// Without the test after the call, the cleanup asks for the old entry and the
// call returns no error.
func TestStagedAddWithASwappedGitDir(t *testing.T) {
	t.Run("before the folder of the add", func(t *testing.T) {
		f := newStagedSwapFixture(t)
		swapAt(t, "prepare", f.gitDir, f.target.dir)
		_, stderr, err := stagedAdd(t, f.repoFolderFixture, f.u)
		want := "cannot prepare the repository's worktrees directory: this repository's git config cannot be used: " +
			quoteOperand(filepath.Join(f.gitDir, "config")) +
			": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to"
		if err == nil || err.Error() != want || stderr != "" {
			t.Errorf("the add = %v, %q\nwant %s", err, stderr, want)
		}
		f.target.untouched(t)
		if len(*f.removals) != 0 {
			t.Errorf("the cleanup asked to remove %q, want nothing", *f.removals)
		}
		if _, err := os.Lstat(filepath.Join(f.gitDir+"-real", "refs", "heads", "w1")); !os.IsNotExist(err) {
			t.Errorf("the branch of a create that was refused before git: %v", err)
		}
		if got := addFolders(t, f.u.folder); len(got) != 0 {
			t.Errorf("the folder of the daemon still holds %q", got)
		}
	})
	t.Run("after the test of that step", func(t *testing.T) {
		f := newStagedSwapFixture(t)
		swapAt(t, "prepared", f.gitDir, f.target.dir)
		_, _, _ = stagedAdd(t, f.repoFolderFixture, f.u)
		f.target.untouched(t)
		if len(*f.removals) != 0 {
			t.Errorf("the cleanup asked to remove %q, want nothing", *f.removals)
		}
	})
	t.Run("after the call", func(t *testing.T) {
		f := newStagedSwapFixture(t)
		swapAt(t, "added", f.gitDir, f.target.dir)
		_, stderr, err := stagedAdd(t, f.repoFolderFixture, f.u)
		var incomplete *addIncomplete
		const want = `".git" is a link or a file where Claude needs a real directory; put the directory itself in its place`
		if !errors.As(err, &incomplete) || incomplete.text != want {
			t.Errorf("the add = %v, %q\nwant the error %s", err, stderr, want)
		}
		f.target.untouched(t)
		if len(*f.removals) != 0 {
			t.Errorf("the cleanup asked to remove %q, want nothing", *f.removals)
		}
		if _, err := os.Lstat(filepath.Join(f.gitDir+"-real", "worktrees", "w1")); !os.IsNotExist(err) {
			t.Errorf("the registration in the real git directory: %v, want none", err)
		}
		if f.u.accepted.moved {
			t.Error("the create counts a registration that did not move")
		}
		if got := addFolders(t, f.u.folder); len(got) != 0 {
			t.Errorf("the folder of the daemon still holds %q", got)
		}
	})
	t.Run("no swap", func(t *testing.T) {
		f := newStagedSwapFixture(t)
		if _, stderr, err := stagedAdd(t, f.repoFolderFixture, f.u); err != nil {
			t.Fatalf("the add failed: %v, %s", err, stderr)
		}
		if got := strings.Join(*f.removals, ","); got != ".claude-new-worktree-real tree" {
			t.Errorf("the cleanup asked to remove %q, want the old entry of the git directory", got)
		}
		if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees", "w1", "HEAD")); err != nil {
			t.Errorf("the registration: %v", err)
		}
	})
}

// TestStagedAddNeedsAHeldGitDir: where the switch is set, a staged add with no
// held git directory does not run. No create comes there.
func TestStagedAddNeedsAHeldGitDir(t *testing.T) {
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	setGitDirMustBeReal(t, true)
	if _, err := worktreeAddStaged(context.Background(), f.useOf(t, f.repo), f.repo, filepath.Join(f.repo, "w1"), worktreeCheckpoint{},
		"worktree", "add", filepath.Join(f.repo, "w1")); err == nil ||
		err.Error() != "cannot prepare the repository's worktrees directory: the create holds no git directory" {
		t.Errorf("the add = %v, want the refusal", err)
	}
}

// TestStagedAddWithAFileAtWorktrees: `worktrees` of the git directory is a
// file. On 5fd08069 the create answers `git worktree add failed: cannot prepare
// the repository's worktrees directory: "C:\\o\\w\\repo\\.git\\worktrees" is not
// a directory`, and the file is unchanged (table W3 of val-s3p2b2-7-windows,
// Windows VM). Without the test of the entry the error is that of a failed
// listing of the folder.
func TestStagedAddWithAFileAtWorktrees(t *testing.T) {
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	worktrees := filepath.Join(f.gitDir, "worktrees")
	if err := os.Rename(worktrees, worktrees+"-kept"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, worktrees, "a file\n")
	_, stderr, err := stagedAdd(t, f, u)
	want := "cannot prepare the repository's worktrees directory: " + quoteOperand(worktrees) + " is not a directory"
	if err == nil || err.Error() != want || stderr != "" {
		t.Errorf("the add = %v, %q\nwant %s", err, stderr, want)
	}
	if got := mustRead(t, worktrees); got != "a file\n" {
		t.Errorf("the file at worktrees = %q", got)
	}
	if got := addFolders(t, u.folder); len(got) != 0 {
		t.Errorf("the folder of the daemon still holds %q", got)
	}
}
