//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestWorktreeCreateUndoOpenHandle pins the step A undo text with the cause
// measured against f6010b97 and 90fca6e6 on a Windows VM: a handle open on
// <leaf>\.git without delete sharing. The rollback of a failed checkout cannot
// delete .git, so it stops there, keeps the registration and w1, and appends the
// step A text with the OS error of the sharing violation. os.Open shares read and
// write only, so the test's own handle is the holder.
func TestWorktreeCreateUndoOpenHandle(t *testing.T) {
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)

	f := newWTFixture(t, false)
	s := newTestServer(t)
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
	// The checkout waits 1.5 s and then fails. The add has written <leaf>\.git by
	// then, and the holder below opens it during the wait.
	slowGit(t, "read-tree", "fail", 1500*time.Millisecond, `fatal: synthetic checkout failure\n`, "")
	done := make(chan struct{})
	opened := make(chan *os.File, 1)
	go func() {
		for {
			if fh, err := os.Open(filepath.Join(f.leaf(), ".git")); err == nil {
				opened <- fh
				return
			}
			select {
			case <-done:
				opened <- nil
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	raw, _ := f.create(t, s, "w1", "", 0)
	close(done)
	fh := <-opened
	if fh == nil {
		t.Fatalf("the holder never opened %s; reply = %s", filepath.Join(f.leaf(), ".git"), raw)
	}
	defer func() { _ = fh.Close() }()
	wantError(t, raw, "git worktree add failed (checkout): fatal: synthetic checkout failure"+
		"; and the undo could not finish for "+f.leaf()+": the worktree directory, its registration, and the branch all remain;"+
		" remove them by hand before retrying (RemoveAll .git: The process cannot access the file because it is being used by another process.)",
		"worktree_add_failed")
	if got := f.leafEntries(t); !slices.Equal(got, []string{".git"}) {
		t.Errorf("leaf entries = %q, want [.git]", got)
	}
	if got := f.regs(t); !slices.Equal(got, []string{"w1"}) {
		t.Errorf("registrations = %q, want w1 kept", got)
	}
	if !f.hasRef(t, "w1") {
		t.Errorf("refs/heads/w1 was deleted, want it kept after a step A failure")
	}
}
