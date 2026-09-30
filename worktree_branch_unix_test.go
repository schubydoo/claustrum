//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Row R23b (Linux and macOS VMs): an update-ref that ignores the SIGTERM at
// updateRefStop is killed updateRefKillAfter later, and the branch stays. The
// reply therefore comes after both waits, not at the first one.
func TestWorktreeRemoveUpdateRefKilledAfterTerm(t *testing.T) {
	realGit := realGitPath(t)
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	f := newRmFixture(t)
	before := refAt(t, realGit, f.repo, "wt1")
	shortBounds(t, 60*time.Second, time.Second, time.Second)
	installGitSlowStub(t, realGit)
	stubRule2(t, "update-ref", "ignoreterm", stubNever, "", "", "")
	start := time.Now()
	raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
	elapsed := time.Since(start)
	if raw != removeKept {
		t.Errorf("reply = %s, want %s", raw, removeKept)
	}
	if elapsed < 2*time.Second || elapsed > stoppedCeiling {
		t.Errorf("reply after %v, want it after the SIGTERM wait and the kill wait (2s) and before %v", elapsed, stoppedCeiling)
	}
	if got := refAt(t, realGit, f.repo, "wt1"); got != before {
		t.Errorf("wt1 = %q, want it kept at %s", got, before)
	}
}

// Row R19 (Linux VM): the entry cannot be dropped, the branch step still runs, and
// it keeps a branch that no other ref reaches. The member follows the error.
func TestWorktreeRemoveEntryDropFailureKeepsBranch(t *testing.T) {
	skipIfRoot(t)
	realGit := realGitPath(t)
	f := newRmFixture(t)
	runGit(t, f.wt, "commit", "-q", "--allow-empty", "-m", "c2")
	c2 := refAt(t, realGit, f.repo, "wt1")
	chmodFor(t, f.entry(), 0o555)
	raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"})
	const want = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"removed the worktree but could not drop its registration (RemoveAll wt1: permission denied)","branchKept":true}}`
	if raw != want {
		t.Errorf("reply = %s\nwant    %s", raw, want)
	}
	mustBeGone(t, f.wt)
	mustExist(t, f.entry())
	if got := refAt(t, realGit, f.repo, "wt1"); got != c2 {
		t.Errorf("wt1 = %q, want it kept at %s", got, c2)
	}
}

// Rows C05 and C08b (Linux VM). C05: the leaf rmdir fails and the branch holds a
// commit that no other ref reaches. The branch part follows the leaf part after
// "; ", without "the worktree itself was removed, but ", and the member is set.
// C08b: in attach mode the leaf clear fails, and the text names no branch, because
// the call made none (f6010b97 and 89cb6289 agree there).
func TestWorktreeCreateRollbackLeafAndBranchTexts(t *testing.T) {
	skipIfRoot(t)
	realGit := realGitPath(t)
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0

	t.Run("C05", func(t *testing.T) {
		f := newWTFixture(t, false)
		runGit(t, f.top, "checkout", "-q", "--detach")
		runGit(t, f.top, "commit", "-q", "--allow-empty", "-m", "c2")
		c2 := f.out(t, f.top, "rev-parse", "HEAD")
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(f.leaf()), 0o755) })
		installGitSlowStub(t, realGit)
		t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
		f.stubAction(t, "lockparent")
		slowGit(t, "read-tree", "fail", 0, `fatal: synthetic checkout failure\n`, "")
		raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
		errText := checkoutFailed + fmt.Sprintf(undoHead, f.leaf()) +
			"the worktree directory remains (re-populated while undoing?); remove it by hand before retrying (removeat w1: permission denied); " +
			undoKeptText
		if want := createFrame(t, errText, "worktree_add_failed", true); raw != want {
			t.Errorf("reply = %s\nwant    %s", raw, want)
		}
		if got := refAt(t, realGit, f.top, "w1"); got != c2 {
			t.Errorf("w1 = %q, want it kept at %s", got, c2)
		}
	})

	t.Run("C08b", func(t *testing.T) {
		f := newWTFixture(t, false)
		ex := refAt(t, realGit, f.top, "ex")
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(f.leaf(), "hsub"), 0o755) })
		installGitSlowStub(t, realGit)
		t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
		f.stubAction(t, "lock")
		slowGit(t, "read-tree", "fail", 0, `fatal: synthetic checkout failure\n`, "")
		raw, _ := f.create(t, newTestServer(t), "w1", "ex", 0)
		errText := checkoutFailed + fmt.Sprintf(undoHead, f.leaf()) +
			"the worktree directory and its registration both remain; remove them by hand before retrying (RemoveAll hsub: permission denied)"
		if want := createFrame(t, errText, "worktree_add_failed", false); raw != want {
			t.Errorf("reply = %s\nwant    %s", raw, want)
		}
		if got := refAt(t, realGit, f.top, "ex"); got != ex {
			t.Errorf("ex = %q, want it kept at %s", got, ex)
		}
		if got := f.regs(t); len(got) != 1 {
			t.Errorf("registrations = %q, want the one of w1 kept", got)
		}
	})
}

// A leaf that the identity guard refuses stays. A kept branch then gets its text
// without "the worktree itself was removed, but ". The stub replaces the leaf with a
// new empty directory during the failed checkout, so the leaf is no longer the one
// the call made. This path is claustrum's own guard (not measured).
func TestWorktreeCreateRollbackKeptBranchWithGuardedLeaf(t *testing.T) {
	realGit := realGitPath(t)
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	f := newWTFixture(t, false)
	runGit(t, f.top, "checkout", "-q", "--detach")
	runGit(t, f.top, "commit", "-q", "--allow-empty", "-m", "c2")
	c2 := f.out(t, f.top, "rev-parse", "HEAD")
	installGitSlowStub(t, realGit)
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
	f.stubAction(t, "repl")
	slowGit(t, "read-tree", "fail", 0, `fatal: synthetic checkout failure\n`, "")
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	errText := checkoutFailed + fmt.Sprintf(undoHead, f.leaf()) + undoKeptText
	if want := createFrame(t, errText, "worktree_add_failed", true); raw != want {
		t.Errorf("reply = %s\nwant    %s", raw, want)
	}
	if got := f.leafEntries(t); got == nil {
		t.Error("the leaf is gone, want the guarded leaf kept")
	}
	if got := refAt(t, realGit, f.top, "w1"); got != c2 {
		t.Errorf("w1 = %q, want it kept at %s", got, c2)
	}
}
