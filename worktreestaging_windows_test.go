//go:build windows

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
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

// seamStagingRename replaces stagingRename until the test ends and counts its
// calls.
func seamStagingRename(t *testing.T, rename func(root *os.Root, oldName, newName string) error) *int {
	t.Helper()
	old := stagingRename
	var mu sync.Mutex
	calls := new(int)
	stagingRename = func(root *os.Root, oldName, newName string) error {
		mu.Lock()
		*calls++
		mu.Unlock()
		return rename(root, oldName, newName)
	}
	t.Cleanup(func() { stagingRename = old })
	return calls
}

// TestCreateHeldStagingFolderOnWindows must match cell W3a of round
// task009-01: the staging folder is held, and released after 300 ms. The
// create answers success, and the registration has its files and the index.
// Without the retry the create fails at the first rename.
func TestCreateHeldStagingFolderOnWindows(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	var held sync.Once
	calls := seamStagingRename(t, func(root *os.Root, oldName, newName string) error {
		held.Do(func() {
			release, err := holdOpenErr(filepath.Join(f.gitDir, oldName), true)
			if err != nil {
				t.Errorf("the hold of the staging folder: %v", err)
				return
			}
			t.Cleanup(release)
			time.AfterFunc(300*time.Millisecond, release)
		})
		return root.Rename(oldName, newName)
	})
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if !strings.Contains(raw, `"result":{"success":true,`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if *calls < 2 {
		t.Errorf("the rename ran %d times, want the retry", *calls)
	}
	for _, name := range []string{"HEAD", "commondir", "gitdir", "index"} {
		if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees", "w1", name)); err != nil {
			t.Errorf("the registration file %s: %v", name, err)
		}
	}
	if got := stagingEntries(t, f.gitDir); len(got) != 0 {
		t.Errorf("the staging entries of the git directory = %q, want none", got)
	}
}

// TestCreateStagingRenameFailsOnWindows must match cells W3b and W3c of round
// task009-01: the rename of the staging folder stays denied. The create
// answers the failure after the retry, also with timeoutMs, and no reply says
// timeout. The leaf, the branch and the staging folder are gone,
// `.git\worktrees` stays, and no line is logged. Without the failure of the
// create the reply is success. Without the retry the rename runs once.
func TestCreateStagingRenameFailsOnWindows(t *testing.T) {
	for _, row := range []struct {
		name   string
		params map[string]any
	}{
		{"no deadline", map[string]any{}},
		{"timeoutMs", map[string]any{"timeoutMs": 50}},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			setRenameRetryBound(t, 300*time.Millisecond)
			calls := seamStagingRename(t, func(_ *os.Root, oldName, newName string) error {
				return &os.LinkError{Op: "renameat", Old: oldName, New: newName, Err: syscall.ERROR_ACCESS_DENIED}
			})
			logs := captureLogBuf(t)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			params := map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}
			for k, v := range row.params {
				params[k] = v
			}
			start := time.Now()
			raw, _ := f.call(t, "git.worktree_create", params)
			took := time.Since(start)
			want := regexp.MustCompile(`"result":\{"success":false,"error":"git worktree add failed \(completing the new worktree's git files\): renameat \.claude-new-worktree-[0-9a-f]{16} worktrees\\\\w1: Access is denied\.","errorCode":"worktree_add_failed"\}\}$`)
			if !want.MatchString(raw) {
				t.Errorf("git.worktree_create = %s\nwant a match of %s", raw, want)
			}
			if *calls < 2 || took < 300*time.Millisecond {
				t.Errorf("the rename ran %d times in %v, want the retry of the bound", *calls, took)
			}
			for _, gone := range []string{leaf, filepath.Join(f.gitDir, "worktrees", "w1"), filepath.Join(f.gitDir, "refs", "heads", "w1")} {
				if _, err := os.Lstat(gone); !os.IsNotExist(err) {
					t.Errorf("%s: %v, want it not there", gone, err)
				}
			}
			if fi, err := os.Lstat(filepath.Join(f.gitDir, "worktrees")); err != nil || !fi.IsDir() {
				t.Errorf("the worktrees folder: %v, want it kept", err)
			}
			if got := stagingEntries(t, f.gitDir); len(got) != 0 {
				t.Errorf("the staging entries of the git directory = %q, want none", got)
			}
			if got := logs.String(); strings.Contains(got, "registration of the new worktree") || strings.Contains(got, "staging folder") {
				t.Errorf("the failed move logged %q, want no line about it", got)
			}
		})
	}
}
