//go:build unix

package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A held call gets SIGTERM at the limit. A call that ignores it gets SIGKILL
// gitStopGrace later, and the reply follows (cell T2 of round task010-01: TERM at
// 120.2 s, KILL at 122.2 s, Linux and macOS VMs). A call that dies by the SIGTERM
// answers before the grace ends. The grace is 3 s here, the limit 1 s.
func TestGitStopTermThenKill(t *testing.T) {
	grace := 3 * time.Second
	t.Run("a call that dies by SIGTERM", func(t *testing.T) {
		f, _ := limitFixture(t, "for-each-ref")
		setDuration(t, &gitStopGrace, grace)
		raw, took := dispatchTimed(t, newTestServer(t), "git.list_branches", map[string]any{"path": f.top})
		if want := errorFrame(t, stopText("for-each-ref")); raw != want {
			t.Fatalf("git.list_branches = %s\nwant %s", raw, want)
		}
		if took >= testCallLimit+grace {
			t.Errorf("the reply took %v, want it before the grace ended (%v): no SIGTERM came first", took, testCallLimit+grace)
		}
	})
	t.Run("a call that ignores SIGTERM", func(t *testing.T) {
		f, _ := limitFixture(t, "")
		stubRule2(t, "for-each-ref", "ignoreterm", testHold, "", "", "")
		setDuration(t, &gitStopGrace, grace)
		raw, took := dispatchTimed(t, newTestServer(t), "git.list_branches", map[string]any{"path": f.top})
		if want := errorFrame(t, stopText("for-each-ref")); raw != want {
			t.Fatalf("git.list_branches = %s\nwant %s", raw, want)
		}
		if took < testCallLimit+grace || took > testFastCeiling+grace {
			t.Errorf("the reply took %v, want it at the SIGKILL, %v after the start", took, testCallLimit+grace)
		}
	})
}

// The read-tree call of a create with no timeoutMs answers 2 s after git exited,
// and the descendant that holds its output gets no signal (cell T16 of round
// task010-01, Linux VM). The descendant lives testDrainHold.
func TestWorktreeCreateCheckoutDrainWithoutTimeoutMs(t *testing.T) {
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	endHeldDescendant(t)
	pidFile := os.Getenv("CLAUSTRUM_GITSTUB_HOLDPID")
	slowGit(t, "read-tree", "hold", testDrainHold, "", "")
	raw, took := f.create(t, newTestServer(t), "w1", "", 0)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if took > testDrainHold-4*time.Second {
		t.Errorf("the create took %v: the daemon waited for the descendant", took)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	// The descendant is a sleep of the test binary. Signal 0 only asks whether it
	// lives, for one second. Its end is left to it.
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("the descendant %d is gone (%v), want it alive: the drain signalled it", pid, err)
		}
	}
}
