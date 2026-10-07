package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A git method on a path that exists and is not a folder. On Windows the start error
// of the configuration listing is a refusal there. 89cb6289 was measured on a Windows
// VM (cells A-01 to A-14). On Linux and macOS the method goes on. These tests set
// package state and the daemon's environment, so none of them runs in parallel.

// gitStartError is the error text of a git call that cannot start in dir. The test
// starts the git of PATH there itself, so the text names that git. It is "" when the
// call starts.
func gitStartError(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "version")
	cmd.Dir = dir
	err := cmd.Run()
	var ee *exec.ExitError
	if err == nil || errors.As(err, &ee) {
		return ""
	}
	if !strings.HasPrefix(err.Error(), "fork/exec ") {
		t.Fatalf("git version in %s: err = %v, want a fork/exec error", dir, err)
	}
	return err.Error()
}

// wantWindowsStartText fails the test on Windows when start, the start error in a
// path that is not a folder, does not end with the text of the cells.
func wantWindowsStartText(t *testing.T, start string) {
	t.Helper()
	if runtime.GOOS == "windows" && !strings.HasSuffix(start, ": The directory name is invalid.") {
		t.Fatalf("start error = %q, want the Windows text of the cells", start)
	}
}

// refuseNonFolderStart sets nonFolderStartRefuses for the test. On Windows that is the
// value of the build.
func refuseNonFolderStart(t *testing.T) {
	t.Helper()
	old := nonFolderStartRefuses
	t.Cleanup(func() { nonFolderStartRefuses = old })
	nonFolderStartRefuses = true
}

// With nonFolderStartRefuses, the start error of the listing in a regular file is the
// hooks refusal in git.info, git.list_branches, git.status (by baseRepo) and
// git.worktree_create (cells A-01 to A-04, A-04b2 and A-06). git.status with the file
// as path only, a path that does not exist, a path under the file and a plain folder
// keep their frames (cells A-04b1, A-07m, A-07s and A-08). Nothing changes on disk.
// Mutation: report a path that is not a folder as unenterable (unenterableDir).
func TestStartErrorInRegularFileRefuses(t *testing.T) {
	f := newNoRepoFixture(t)
	refuseNonFolderStart(t)
	nFile := filepath.Join(f.n, "n.txt")
	writeFile(t, nFile, "n\n", 0o644)
	tFile := filepath.Join(f.top, "t.txt")
	start := gitStartError(t, nFile)
	if start == "" {
		t.Fatalf("git started in the regular file %s", nFile)
	}
	wantWindowsStartText(t, start)
	text := hooksRefusalPrefix + start
	leaf := filepath.Join(nFile, ".claude", "worktrees", "w1")
	for _, row := range []struct {
		cell, method string
		params       map[string]any
		want         string
	}{
		{"A-01", "git.info", map[string]any{"path": nFile}, errFrame(t, text)},
		{"A-02", "git.info", map[string]any{"path": tFile}, errFrame(t, text)},
		{"A-03", "git.list_branches", map[string]any{"path": nFile}, errFrame(t, text)},
		{"A-04", "git.status", map[string]any{"path": nFile, "baseRepo": nFile}, errFrame(t, text)},
		{"A-04b2", "git.status", map[string]any{"path": f.top, "baseRepo": nFile}, errFrame(t, text)},
		{"A-06", "git.worktree_create", map[string]any{"baseRepo": nFile, "branchName": "w1", "worktreePath": leaf},
			resultFrame(t, worktreeResult{Success: false, Error: text, ErrorCode: "worktree_add_failed"})},
		{"A-04b1", "git.status", map[string]any{"path": nFile, "baseRepo": f.top}, resultOf(notRepoStatus)},
		{"A-07m", "git.info", map[string]any{"path": f.m}, resultOf(notRepoInfo)},
		{"A-07s", "git.info", map[string]any{"path": filepath.Join(nFile, "sub")}, resultOf(notRepoInfo)},
		{"A-08", "git.info", map[string]any{"path": f.n}, resultOf(notRepoInfo)},
	} {
		if got := f.frame(t, row.method, row.params); got != row.want {
			t.Errorf("%s %s = %s\nwant %s", row.cell, row.method, got, row.want)
		}
	}
	if got := dirNames(t, f.n); len(got) != 1 || got[0] != "n.txt" {
		t.Errorf("the plain folder holds %q, want n.txt alone", got)
	}
	if exists(f.m) {
		t.Errorf("a request made %s", f.m)
	}
}

// resultOf is the reply frame with the result member result.
func resultOf(result string) string {
	return `{"jsonrpc":"2.0","id":1,"result":` + result + `}`
}

// Cell A-14. When `git version` fails too, the regular file gets the "cannot run" text
// with the detail of the version call. Mutation: report a path that is not a folder
// as unenterable (unenterableDir).
func TestStartErrorInRegularFileWithFailingVersion(t *testing.T) {
	f := newNoRepoFixture(t)
	refuseNonFolderStart(t)
	nFile := filepath.Join(f.n, "n.txt")
	writeFile(t, nFile, "n\n", 0o644)
	versionFails(t)
	if got, want := f.frame(t, "git.info", map[string]any{"path": nFile}), errFrame(t, versionFailsText); got != want {
		t.Errorf("A-14 = %s\nwant %s", got, want)
	}
}

// Cell A-10 (Windows VM). With nonFolderStartRefuses, a file symlink as path gets the
// hooks refusal with the start error of the listing, as a regular file does. The test
// skips when the user cannot make a symlink. Mutation: report a path that is not a
// folder as unenterable (unenterableDir).
func TestStartErrorInFileSymlinkRefuses(t *testing.T) {
	f := newNoRepoFixture(t)
	refuseNonFolderStart(t)
	nFile := filepath.Join(f.n, "n.txt")
	writeFile(t, nFile, "n\n", 0o644)
	lnk := filepath.Join(f.n, "lnk")
	if err := os.Symlink(nFile, lnk); err != nil {
		t.Skipf("no symlink on this host: %v", err)
	}
	start := gitStartError(t, lnk)
	if start == "" {
		t.Fatalf("git started in the file symlink %s", lnk)
	}
	wantWindowsStartText(t, start)
	if got, want := f.frame(t, "git.info", map[string]any{"path": lnk}), errFrame(t, hooksRefusalPrefix+start); got != want {
		t.Errorf("A-10 = %s\nwant %s", got, want)
	}
}
