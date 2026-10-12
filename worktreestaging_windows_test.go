//go:build windows

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The Windows side of the staged `worktree add` and of git.status through a
// junction. No test of this file ran on the host of the build: a Windows run
// must match the cell that each test names. TestCreateStagesTheAddOnWindowsOnly
// has the good create (cells 44a, 49a and control M1 of cell 48).

// TestCreateFailedAddOnWindows must match cells 44b and 44d of
// s3p2b-edges2-windows: the wrapper answers exit 1 for the `worktree add`. The
// reply is the failed add, `.s<n>` is gone, and the new worktree folder, the
// branch and the registration do not exist. The cleanup is not measured for a
// failed create: the old staging entry stays.
func TestCreateFailedAddOnWindows(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	old := plantStagingEntry(t, f.gitDir, ".claude-new-worktree-old1", 2*time.Hour)
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "1")
	slowGit(t, "worktree,add", "fail", 0, `wrapper: forced failure\n`, "")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	const want = `"result":{"success":false,"error":"git worktree add failed: wrapper: forced failure","errorCode":"worktree_add_failed"}`
	if !strings.Contains(raw, want) {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	folder := filepath.Join(f.run, shadowRootName, daemonFolders(t, f.run)[0])
	if add := commonDirOf(t, calls, "worktree", "add"); !strings.HasPrefix(filepath.Base(add), addCopyPrefix) || filepath.Dir(add) != folder {
		t.Errorf("the add call has GIT_COMMON_DIR=%s, want .s<n> in %s", add, folder)
	}
	if got := addFolders(t, folder); len(got) != 0 {
		t.Errorf("the folder of the daemon still holds %q", got)
	}
	for _, gone := range []string{leaf, filepath.Join(f.gitDir, "worktrees", "w1"), filepath.Join(f.gitDir, "refs", "heads", "w1")} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("%s: %v, want it not there", gone, err)
		}
	}
	if _, err := os.Lstat(old); err != nil {
		t.Errorf("the old staging entry after a failed add: %v, want it kept", err)
	}
}

// TestCreateFailedReadTreeOnWindows must match cells 44c and 44e of
// s3p2b-edges2-windows: the add is real and the wrapper answers exit 1 for the
// `read-tree`. The reply is the failed checkout. The registration, the new
// worktree folder, the branch, `.s<n>` and `.index-<n>` are gone, and no
// staging folder is left.
func TestCreateFailedReadTreeOnWindows(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "1")
	slowGit(t, "read-tree", "fail", 0, `wrapper: forced failure\n`, "")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	const want = `"result":{"success":false,"error":"git worktree add failed (checkout): wrapper: forced failure","errorCode":"worktree_add_failed"}`
	if !strings.Contains(raw, want) {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	for _, gone := range []string{leaf, filepath.Join(f.gitDir, "worktrees", "w1"), filepath.Join(f.gitDir, "refs", "heads", "w1")} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("%s: %v, want it not there", gone, err)
		}
	}
	folder := filepath.Join(f.run, shadowRootName, daemonFolders(t, f.run)[0])
	ents, err := os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), addCopyPrefix) || strings.HasPrefix(e.Name(), shadowIndexDirPrefix) {
			t.Errorf("the folder of the daemon still holds %s", e.Name())
		}
	}
	if got := stagingEntries(t, f.gitDir); len(got) != 0 {
		t.Errorf("the git directory still holds %q", got)
	}
}

// TestSweepRemovesAJunctionAsALink must match the junction rows of cell 48 of
// s3p2b-edges3-windows: an old junction with the name form goes, and its target
// and the marker in the target stay. The junction is new and the clock of the
// cleanup is two hours ahead, so the junction is old. The target has a write
// time that is young for that clock: "junction old and target new: gone".
func TestSweepRemovesAJunctionAsALink(t *testing.T) {
	f := newStagingSweepFixture(t)
	link := filepath.Join(f.dir, ".claude-new-worktree-0123456789abcdee")
	makeJunction(t, link, f.target)
	remove := removeStagingEntry
	calls := recordStagingRemovals(t)
	f.sweep(t)
	if !slices.Contains(*calls, ".claude-new-worktree-0123456789abcdee entry") {
		t.Errorf("the cleanup asked to remove %q, want the junction as one entry", *calls)
	}
	removeStagingEntry = remove
	f.sweep(t)
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("the junction: %v, want it gone", err)
	}
	if text := mustRead(t, filepath.Join(f.target, "marker.txt")); text != "marker of the junction target\n" {
		t.Errorf("the marker in the target of the junction = %q", text)
	}
}

// TestStatusThroughAJunction must match cell D6j-junction of
// val-s3p2b2-3-windows, request 2: a git.status whose `path` and `baseRepo` go
// through a junction to the repository. The reply names a repository. The
// daemon has two folders of a repository: the one of the junction path and the
// one of the real path. Each call with --git-dir=<git directory> names the real
// git directory and carries GIT_COMMON_DIR=<the folder of the real path> and
// GIT_OBJECT_DIRECTORY=<real git directory>\objects.
func TestStatusThroughAJunction(t *testing.T) {
	f := newSeededShadowFixture(t)
	jrepo := filepath.Join(resolveTestRoot(t, t.TempDir()), "jrepo")
	makeJunction(t, jrepo, f.repo)
	if raw, _ := f.info(t, jrepo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info through the junction = %s", raw)
	}
	raw, calls := f.call(t, "git.status", map[string]any{
		"path": filepath.Join(jrepo, ".claude", "worktrees", "wr"), "baseRepo": jrepo})
	if !strings.Contains(raw, `"result":{"isRepo":true,`) {
		t.Errorf("git.status through the junction = %s, want a repository", raw)
	}
	folder := filepath.Join(f.run, shadowRootName, daemonFolders(t, f.run)[0])
	viaJunction := shadowRepoName(filepath.Join(jrepo, ".git"))
	real := shadowRepoName(f.gitDir)
	for _, name := range []string{viaJunction, real} {
		if _, err := os.Lstat(filepath.Join(folder, name, "config")); err != nil {
			t.Errorf("the folder %s of a repository: %v", name, err)
		}
	}
	n := 0
	for _, c := range calls {
		if !slices.Contains(c.argv, "--git-dir="+f.gitDir) {
			continue
		}
		n++
		if v, _ := envValue(c.env, "GIT_COMMON_DIR"); v != filepath.Join(folder, real) {
			t.Errorf("the call %q has GIT_COMMON_DIR=%s, want %s", c.argv, v, filepath.Join(folder, real))
		}
		if v, _ := envValue(c.env, "GIT_OBJECT_DIRECTORY"); v != filepath.Join(f.gitDir, "objects") {
			t.Errorf("the call %q has GIT_OBJECT_DIRECTORY=%s, want %s", c.argv, v, filepath.Join(f.gitDir, "objects"))
		}
	}
	if n == 0 {
		t.Error("no call names the real git directory with --git-dir=")
	}
}
