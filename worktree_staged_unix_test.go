//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The staged add of git.worktree_create on Linux and macOS (be1c1ca5),
// through the server with the git of the host.

// secondRepoGitDir makes a second repository with one commit and returns its
// git directory.
func secondRepoGitDir(t *testing.T) string {
	t.Helper()
	other := filepath.Join(resolveTestRoot(t, t.TempDir()), "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "init", "-q", "-b", "main")
	mustWrite(t, filepath.Join(other, "o.txt"), "o\n")
	runGit(t, other, "add", "o.txt")
	runGit(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "o")
	return filepath.Join(other, ".git")
}

// TestStagedCreateWithAnotherRepositoryAfterTheAdd: right after the add the
// `.git` of a second repository is put at the path. The staging step takes
// the hold on it and moves the registration there. The checkout then fails,
// because that repository has no branch w1. be1c1ca5 answers git's text and
// removes the registration in that folder (measured on Linux), and so does
// claustrum, by the identity of the moved entry. The leaf goes, and the
// branch stays in the old folder. Without the moved registration in the
// rollback the registration stays.
func TestStagedCreateWithAnotherRepositoryAfterTheAdd(t *testing.T) {
	f := newSeededShadowFixture(t)
	other := secondRepoGitDir(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	atStep(t, "staged", func() {
		if err := os.Rename(f.gitDir, f.gitDir+"-real"); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(other, f.gitDir); err != nil {
			t.Error(err)
		}
	})
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	got := createError(t, raw)
	if !strings.HasPrefix(got, "git worktree add failed (checkout): ") || strings.Contains(got, "undo could not finish") {
		t.Errorf("the error = %s\nwant the checkout text alone", got)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees", "w1")); !os.IsNotExist(err) {
		t.Errorf("the registration in the second repository: %v, want it gone", err)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir+"-real", "refs", "heads", "w1")); err != nil {
		t.Errorf("the branch in the old folder: %v, want it kept", err)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
}

// TestStagedCreateWithACopyAfterTheAdd: right after the add a copy of `.git`
// is put at the path. be1c1ca5 succeeds, and the leaf and its registration
// are in the new folder (measured on Linux). Without the replacement step the
// create fails with the text of a git directory that is no real directory. An
// old staging entry of the copy stays: the sweep is skipped after the hold
// moved (claustrum's own). Without that skip it goes.
func TestStagedCreateWithACopyAfterTheAdd(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	atStep(t, "staged", func() {
		copied := f.gitDir + ".copy"
		if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(f.gitDir, f.gitDir+"-real"); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(copied, f.gitDir); err != nil {
			t.Error(err)
		}
		plantStagingEntry(t, f.gitDir, ".claude-new-worktree-old", 2*time.Hour)
	})
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, ".claude-new-worktree-old", "f.txt")); err != nil {
		t.Errorf("the old staging entry of the copy: %v, want it kept", err)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees", "w1", "index")); err != nil {
		t.Errorf("the registration in the new folder: %v, want it there", err)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir+"-real", "worktrees", "w1")); !os.IsNotExist(err) {
		t.Errorf("the registration in the old folder: %v, want none", err)
	}
}

// TestStagedCreateWithALinkBeforeTheAdd: a symbolic link to a copy of `.git`
// is put at the path while git starts the add. be1c1ca5 answers the text of a
// link and removes the leaf, and it deletes the branch through the link
// (measured on Linux). claustrum gives the same text and keeps the branch
// that git made through the link. Without the test of the branch step the
// branch goes.
func TestStagedCreateWithALinkBeforeTheAdd(t *testing.T) {
	f := newSeededShadowFixture(t)
	copied := filepath.Join(resolveTestRoot(t, t.TempDir()), "copy")
	if err := os.CopyFS(copied, os.DirFS(f.gitDir)); err != nil {
		t.Fatal(err)
	}
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
	t.Setenv("CLAUSTRUM_GITSTUB_VALUE", "rename\x1f"+f.gitDir+"\x1f"+f.gitDir+"-real\x1esymlink\x1f"+copied+"\x1f"+f.gitDir)
	slowGit(t, "worktree,add", "pre", 0, "", "")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if got, want := createError(t, raw), completingAddPrefix+realDirText(f.gitDir); got != want {
		t.Errorf("the error = %s\n    want %s", got, want)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
	if _, err := os.Lstat(filepath.Join(copied, "refs", "heads", "w1")); err != nil {
		t.Errorf("the branch behind the link: %v, want it kept", err)
	}
}

// TestStagedCreateWithALockedFileThatStays: the file `locked` of the moved
// registration cannot be removed. be1c1ca5 answers `git worktree add failed
// (completing the new worktree's git files): removeat locked: <cause>; try
// again`, keeps the entry with `locked`, and the leaf and the branch go
// (measured on Linux and macOS). The test also reads `locked` in the staging
// folder before the rename: `initializing` and a newline. Without
// keepRegistration the rollback removes the entry.
func TestStagedCreateWithALockedFileThatStays(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	oldRename := stagingRename
	t.Cleanup(func() { stagingRename = oldRename })
	lock := ""
	stagingRename = func(root *os.Root, oldName, newName string) error {
		if b, err := root.ReadFile(filepath.Join(oldName, "locked")); err == nil {
			lock = string(b)
		}
		return oldRename(root, oldName, newName)
	}
	oldRemove := removeLockedFile
	t.Cleanup(func() { removeLockedFile = oldRemove })
	removeLockedFile = func(*os.Root) error {
		return &os.PathError{Op: "removeat", Path: "locked", Err: errors.New("operation not permitted")}
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := completingAddPrefix + "removeat locked: operation not permitted; try again"
	if got := createError(t, raw); got != want {
		t.Errorf("the error = %s\n    want %s", got, want)
	}
	if lock != initializingLock {
		t.Errorf("locked in the staging folder = %q, want %q", lock, initializingLock)
	}
	for _, name := range []string{"HEAD", "commondir", "gitdir", "locked"} {
		if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees", "w1", name)); err != nil {
			t.Errorf("%s of the entry: %v, want it kept", name, err)
		}
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "w1")); !os.IsNotExist(err) {
		t.Errorf("the branch: %v, want it gone", err)
	}
}

// TestStagedCreateWithATrailingBlank: the leaf `b ` with a trailing blank.
// be1c1ca5 keeps the blank in the reply path and in the `gitdir` record, git
// names the entry `b-`, and the `.git` file of the leaf names `worktrees/b-`
// (measured on Linux).
func TestStagedCreateWithATrailingBlank(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "b ")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "b1", "worktreePath": leaf})
	if !strings.Contains(raw, `"success":true`) || !strings.Contains(raw, `"path":"`+leaf+`"`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	if got, want := mustRead(t, filepath.Join(f.gitDir, "worktrees", "b-", "gitdir")), leaf+"/.git\n"; got != want {
		t.Errorf("the gitdir record = %q, want %q", got, want)
	}
	if got, want := mustRead(t, filepath.Join(leaf, ".git")), worktreeGitFileText(f.gitDir, "b-"); got != want {
		t.Errorf("the .git file of the leaf = %q, want %q", got, want)
	}
}

// TestStagedEntryModes: a `--shared=group` repository with no registration,
// under umask 002. On Linux be1c1ca5 gives the new entry 2775 and `HEAD` 664,
// and the staging step gives the entry the mode that git gave its source
// (stagedEntryModes). On macOS be1c1ca5 gives the entry `drwxr-xr-x` with no
// set-group-ID bit and `HEAD` `-rw-rw-r--` (measured on Linux and macOS).
// Without stagedEntryModes the entry and `HEAD` keep the modes of their
// create.
func TestStagedEntryModes(t *testing.T) {
	f := newSeededShadowFixture(t)
	dropFixtureWorktree(t, f)
	runGit(t, f.repo, "config", "core.sharedRepository", "group")
	useUmask(t, 0o002)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	if raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	mode := func(p string) os.FileMode {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode() & (os.ModePerm | os.ModeSetgid)
	}
	reg := filepath.Join(f.gitDir, "worktrees", "w1")
	want := os.FileMode(0o755)
	if stagedEntryTakesWorktreesGroup {
		want = 0o775 | os.ModeSetgid
	}
	if got := mode(reg); got != want {
		t.Errorf("the entry has mode %v, want %v", got, want)
	}
	if got := mode(filepath.Join(reg, "HEAD")); got != 0o664 {
		t.Errorf("HEAD has mode %v, want 0664", got)
	}
}

// TestCopyStagedModesSkipsOtherKinds: copyStagedModes gives no mode for an
// entry of the source that is neither a regular file nor a folder, here a
// symbolic link. Without the skip the entry of the same name gets the mode of
// the link.
func TestCopyStagedModesSkipsOtherKinds(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.Symlink(filepath.Join(src, "missing"), filepath.Join(src, "l")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dst, "s", "l"), "x\n")
	if err := os.Chmod(filepath.Join(dst, "s", "l"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyStagedModes(openTestRoot(t, src), openTestRoot(t, dst), "s", ""); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(dst, "s", "l"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("the entry has mode %o, want 600", got)
	}
}

// swapInSecondRepo puts the `.git` of other at the git directory of f when the
// create reaches the staging step, and fails the read-tree call of the
// checkout. It returns the path of the old git directory.
func swapInSecondRepo(t *testing.T, f shadowFixture, other string) string {
	t.Helper()
	old := f.gitDir + "-real"
	atStep(t, "staged", func() {
		if err := os.Rename(f.gitDir, old); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(other, f.gitDir); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "1")
	slowGit(t, "read-tree", "fail", 0, `wrapper: forced failure\n`, "")
	return old
}

// TestStagedCreateKeepsTheBranchOfASecondRepository: the second repository
// that takes the hold right after the add has its own branch w1, with another
// tip that main reaches. The checkout fails. The rollback deletes only the
// branch that this create made, so the branch of the second repository stays
// and the reply has no clause for it (claustrum's own, not measured on
// be1c1ca5). Without the test of the tip it goes.
func TestStagedCreateKeepsTheBranchOfASecondRepository(t *testing.T) {
	f := newSeededShadowFixture(t)
	other := secondRepoGitDir(t)
	runGit(t, filepath.Dir(other), "branch", "w1")
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	old := swapInSecondRepo(t, f, other)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if got := createError(t, raw); got != "git worktree add failed (checkout): wrapper: forced failure" {
		t.Errorf("the error = %s, want the checkout text alone", got)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "w1")); err != nil {
		t.Errorf("the branch of the second repository: %v, want it kept", err)
	}
	if _, err := os.Lstat(filepath.Join(old, "refs", "heads", "w1")); err != nil {
		t.Errorf("the branch in the old folder: %v, want it kept", err)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees", "w1")); !os.IsNotExist(err) {
		t.Errorf("the moved registration: %v, want it gone", err)
	}
}

// TestStagedCreateKeepsAFolderThatReplacedTheEntryInASecondRepository: after
// the hold moved to a second repository, a new folder takes the name of the
// moved entry before the checkout, which fails. The rollback removes only the
// entry with the identity of the staging folder, so the new folder stays
// (D24). Without the identity test it goes.
func TestStagedCreateKeepsAFolderThatReplacedTheEntryInASecondRepository(t *testing.T) {
	f := newSeededShadowFixture(t)
	other := secondRepoGitDir(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	swapInSecondRepo(t, f, other)
	oldHook := createStepHook
	createStepHook = func(at string) {
		if oldHook != nil {
			oldHook(at)
		}
		if at != "checkout" {
			return
		}
		reg := filepath.Join(f.gitDir, "worktrees", "w1")
		if err := os.Rename(reg, reg+"x"); err != nil {
			t.Error(err)
			return
		}
		mustWrite(t, filepath.Join(reg, "f.txt"), "other\n")
	}
	t.Cleanup(func() { createStepHook = oldHook })
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if got := mustRead(t, filepath.Join(f.gitDir, "worktrees", "w1", "f.txt")); got != "other\n" {
		t.Errorf("the folder at w1 = %q, want it kept", got)
	}
}
