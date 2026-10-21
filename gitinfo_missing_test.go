package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// The start of the listing of a git.info on a missing path fails before a
// process starts: the error is that of the folder test of os.StartProcess,
// "chdir", not the "fork/exec" error of a start that failed. 5fd08069 starts no
// process there (cell T7s of round task107-01, Linux VM). Mutation: in
// runListing, set cmd.SysProcAttr = newSysProcAttr(). The start then forks on
// Linux and macOS, and the error is "fork/exec". On Windows the error is a
// "fork/exec" error too, and not ErrNotExist (from the code of Go).
func TestMissingPathListingStartsNoProcess(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git in PATH")
	}
	dir := filepath.Join(t.TempDir(), "nope")
	err := missingPathListing(dir)
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Op != "chdir" || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the start in a missing folder = %v, want the chdir error of the folder test", err)
	}
}

// A git.info on a missing path takes the steps of a folder with no repository
// up to the start of its config listing (claustrum's own, not measured). The test
// makes the folder at the second resolve of the path: the first one is the trust
// check, the second the pin of the listing. The listing then starts in it, and
// the reply stays the "no repository" answer. Mutation: in gitInfo, return
// notRepo() with no call of missingPathListing. No listing then runs.
func TestMissingPathInfoReachesTheListingStart(t *testing.T) {
	f := newShadowFixture(t)
	dir, resolves := appearAtSecondResolve(t)
	raw, calls := f.info(t, dir, nil)
	if raw != noRepoLine {
		t.Errorf("git.info = %s\nwant %s", raw, noRepoLine)
	}
	listed := false
	for _, c := range calls {
		if sameDir(c.cwd, dir) {
			if !stubMatches(c.argv, "config,-z,--list") {
				t.Errorf("a call in the folder is %q, want the config listing only", c.argv)
			}
			listed = true
		}
	}
	if !listed {
		t.Errorf("no config listing ran in the folder (%d resolves), want the start of the listing", resolves.Load())
	}
}

// appearAtSecondResolve is a missing path that becomes a folder at its second
// resolve by the walk of the trust check, and the count of those resolves.
func appearAtSecondResolve(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	dir := filepath.Join(resolveTestRoot(t, t.TempDir()), "nope")
	resolves := new(atomic.Int32)
	old := resolveWalkLinks
	t.Cleanup(func() { resolveWalkLinks = old })
	resolveWalkLinks = func(p string) (string, error) {
		if p == dir && resolves.Add(1) == 2 {
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Error(err)
			}
		}
		return old(p)
	}
	return dir, resolves
}

// A listing of a missing path that its time limit stops does not change the
// reply. The folder appears before the start, and the stub holds the listing
// past the shortened limit (claustrum's own, not measured). Mutation: in
// missingPathListing, use a gitUse with no stop of its own and pass it the stop
// of the request. The reply is then the stop text.
func TestMissingPathListingStopKeepsTheReply(t *testing.T) {
	f := newShadowFixture(t)
	dir, _ := appearAtSecondResolve(t)
	t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "hold-other")
	setDuration(t, &configListingLimit, testCallLimit)
	if raw, _ := f.info(t, dir, nil); raw != noRepoLine {
		t.Errorf("git.info = %s\nwant %s", raw, noRepoLine)
	}
}
