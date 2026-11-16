//go:build linux || darwin

package git

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// lingeringReadTree is a read-tree call of the git-lingering stub with a deadline
// stop. It exits as exit0 says, and its child, in the group of git, holds its
// pipes for 30 s. The child also holds the write end of a pipe of the test. The
// returned read end sees EOF when the child ends. The deadline is 1 min, so no
// deadline fires unless the test ends ctx. afterExit runs right after the wait
// for the exit of git, before the gate closes.
func lingeringReadTree(t *testing.T, ctx context.Context, exit0 string, afterExit func(*exec.Cmd)) (*exec.Cmd, *checkoutStop, *os.File) {
	t.Helper()
	endHeldDescendant(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	held, holder, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close(); _ = holder.Close() })
	cmd := exec.CommandContext(ctx, exe, "read-tree")
	cmd.Env = buildEnv(map[string]string{
		"CLAUSTRUM_TEST_HELPER":    "git-lingering",
		"CLAUSTRUM_GITSTUB_EXIT0":  exit0,
		"CLAUSTRUM_GITSTUB_ORPHAN": "30",
	})
	cmd.ExtraFiles = []*os.File{holder}
	cmd.SysProcAttr = sysproc.NewSysProcAttr()
	cmd.WaitDelay = time.Second
	st := newCheckoutStop()
	cmd.Cancel = deadlineStop(cmd, st)
	prev := leaderExitWait
	t.Cleanup(func() { leaderExitWait = prev })
	leaderExitWait = func(pid int) (bool, bool) {
		// git started: only the child keeps the write end open now.
		_ = holder.Close()
		seen, clean := prev(pid)
		if afterExit != nil {
			afterExit(cmd)
		}
		return seen, clean
	}
	return cmd, st, held
}

// groupKillsAtReap replaces reapCheckoutGroup and checkoutWaited. The fake
// counts the group kills and those that came before the reap of git. With
// real set, a kill before the reap goes to the group of git, a child of this
// test. A kill after the reap sends nothing.
func groupKillsAtReap(t *testing.T, real bool) (all, beforeReap *atomic.Int32) {
	t.Helper()
	all, beforeReap = new(atomic.Int32), new(atomic.Int32)
	var waited atomic.Bool
	prevReap, prevWaited := reapCheckoutGroup, checkoutWaited
	t.Cleanup(func() { reapCheckoutGroup, checkoutWaited = prevReap, prevWaited })
	checkoutWaited = func(*exec.Cmd) { waited.Store(true) }
	reapCheckoutGroup = func(p *os.Process) {
		all.Add(1)
		if waited.Load() {
			return
		}
		beforeReap.Add(1)
		if real {
			sysproc.ReapProcessGroup(p)
		}
	}
	return all, beforeReap
}

// endsWithin reports that r sees EOF within limit.
func endsWithin(r io.Reader, limit time.Duration) bool {
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, r); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(limit):
		return false
	}
}

func noSkip(error) bool { return false }

// TestDrainOverrunKillsGroupBeforeReap: git exits 0 and its child holds the
// pipes past the cap. The group kill comes once, before the reap of git, and it
// ends the child (claustrum's own, from the code).
func TestDrainOverrunKillsGroupBeforeReap(t *testing.T) {
	all, beforeReap := groupKillsAtReap(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd, st, held := lingeringReadTree(t, ctx, "1", nil)
	var errBuf bytes.Buffer
	if err := runCheckoutPipes(cmd, &errBuf, noSkip, nil, st, nil); !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("read-tree = %v, want exec.ErrWaitDelay", err)
	}
	if n, b := all.Load(), beforeReap.Load(); n != 1 || b != 1 {
		t.Errorf("group kills = %d, before the reap = %d, want 1 and 1", n, b)
	}
	if !endsWithin(held, 10*time.Second) {
		t.Error("the child of git still holds its pipe 10 s after the reply: the group kill did not reach it")
	}
}

// TestDrainOverrunAfterFailedExitSendsNoKill: git fails and its child holds
// stdout past the cap. The wait comes first, and the overrun sends no group
// kill (claustrum's own, from the code). On Linux the failed exit gets its one
// kill before the reap (failedCheckoutKillsGroup).
func TestDrainOverrunAfterFailedExitSendsNoKill(t *testing.T) {
	all, beforeReap := groupKillsAtReap(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd, st, _ := lingeringReadTree(t, ctx, "fail", nil)
	var errBuf bytes.Buffer
	var exitErr *exec.ExitError
	if err := runCheckoutPipes(cmd, &errBuf, noSkip, nil, st, nil); !errors.As(err, &exitErr) {
		t.Fatalf("read-tree = %v, want the exit error of git", err)
	}
	want := int32(0)
	if failedCheckoutKillsGroup {
		want = 1
	}
	if n, b := all.Load(), beforeReap.Load(); n != want || b != want {
		t.Errorf("group kills = %d, before the reap = %d, want %d and %d", n, b, want, want)
	}
}

// TestStopDuringDrainBeforeReapDoesNothing: the deadline fires during a drain
// that runs before the reap. The stop marks nothing and sends nothing, and the
// drain still ends in exec.ErrWaitDelay, as when the wait came first (claustrum's
// own, from the code).
func TestStopDuringDrainBeforeReapDoesNothing(t *testing.T) {
	all, beforeReap := groupKillsAtReap(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, st, _ := lingeringReadTree(t, ctx, "1", func(*exec.Cmd) {
		time.AfterFunc(200*time.Millisecond, cancel)
	})
	var errBuf bytes.Buffer
	if err := runCheckoutPipes(cmd, &errBuf, noSkip, nil, st, nil); !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("read-tree = %v, want exec.ErrWaitDelay", err)
	}
	if st.stopped() {
		t.Error("the stop in the drain marked the stop")
	}
	if n, b := all.Load(), beforeReap.Load(); n != 1 || b != 1 {
		t.Errorf("group kills = %d, before the reap = %d, want only the kill of the overrun", n, b)
	}
}

// TestStopBeforeDrainKeepsWaitFirst: a stop that runs after the exit of git and
// before the drain keeps the old order. The overrun then sends no group kill.
// The test runs the stop itself, so the wait of exec.Cmd gets no error from it
// (claustrum's own, from the code).
func TestStopBeforeDrainKeepsWaitFirst(t *testing.T) {
	all, _ := groupKillsAtReap(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd, st, _ := lingeringReadTree(t, ctx, "1", func(c *exec.Cmd) { _ = c.Cancel() })
	var errBuf bytes.Buffer
	if err := runCheckoutPipes(cmd, &errBuf, noSkip, nil, st, nil); !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("read-tree = %v, want exec.ErrWaitDelay", err)
	}
	if !st.stopped() {
		t.Error("the stop did not mark the stop")
	}
	if n := all.Load(); n != 1 {
		t.Errorf("group kills = %d, want 1: the one of the stop and none for the overrun", n)
	}
}
