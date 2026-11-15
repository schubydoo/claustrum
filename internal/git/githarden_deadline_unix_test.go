//go:build linux || darwin

package git

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// countGroupKills replaces reapCheckoutGroup with a fake that counts its calls
// and sends nothing. The kill through the handle of git still ends git.
func countGroupKills(t *testing.T) func() int {
	t.Helper()
	var mu sync.Mutex
	kills := 0
	prev := reapCheckoutGroup
	t.Cleanup(func() { reapCheckoutGroup = prev })
	reapCheckoutGroup = func(*os.Process) {
		mu.Lock()
		kills++
		mu.Unlock()
	}
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return kills
	}
}

// stdinGit is a git call that runs until feed closes. It leads its own group.
func stdinGit(t *testing.T, ctx context.Context) (*exec.Cmd, *os.File) {
	t.Helper()
	in, feed, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = feed.Close() })
	cmd := exec.CommandContext(ctx, "git", "hash-object", "--stdin")
	cmd.Dir, cmd.Stdin = t.TempDir(), in
	cmd.SysProcAttr = sysproc.NewSysProcAttr()
	cmd.WaitDelay = time.Second
	return cmd, feed
}

// TestDeadlineStopAfterReapSendsNoGroupKill: Go runs cmd.Cancel from its watch
// of the context, which can come after the wait reaped git. That call sends
// no group kill (claustrum's own, from the code). The test makes the late call
// itself, after runCheckoutPipes returned.
func TestDeadlineStopAfterReapSendsNoGroupKill(t *testing.T) {
	kills := countGroupKills(t)
	cmd, feed := stdinGit(t, context.Background())
	gate := &leaderGate{}
	var cancelled atomic.Bool
	stop := deadlineStop(cmd, gate, &cancelled)
	_ = feed.Close()
	var errBuf bytes.Buffer
	if err := runCheckoutPipes(cmd, &errBuf, func(error) bool { return false }, nil, gate); err != nil {
		t.Fatalf("git hash-object = %v, %s", err, errBuf.String())
	}
	if err := stop(); err != nil {
		t.Fatalf("the late stop = %v, want nil", err)
	}
	if n := kills(); n != 0 {
		t.Errorf("group kills after the reap = %d, want 0", n)
	}
	if !cancelled.Load() {
		t.Error("the late stop did not mark the stop")
	}
}

// TestDeadlineStopWhileGitRunsKillsGroup: a deadline that fires while git runs
// still sends one group kill, before the reap. The deadline of 1 s leaves time
// for the start of git on a loaded host.
func TestDeadlineStopWhileGitRunsKillsGroup(t *testing.T) {
	kills := countGroupKills(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd, _ := stdinGit(t, ctx)
	gate := &leaderGate{}
	var cancelled atomic.Bool
	cmd.Cancel = deadlineStop(cmd, gate, &cancelled)
	var errBuf bytes.Buffer
	err := runCheckoutPipes(cmd, &errBuf, func(error) bool { return false }, nil, gate)
	if err == nil {
		t.Fatal("git hash-object ended well, want the kill of the deadline")
	}
	if n := kills(); n != 1 {
		t.Errorf("group kills = %d, want 1", n)
	}
	if !cancelled.Load() {
		t.Error("the deadline did not mark the stop")
	}
}

// failLeaderExitWait makes the wait for the exit of git fail at once, as a
// kqueue that cannot be made does on macOS.
func failLeaderExitWait(t *testing.T) {
	t.Helper()
	prev := leaderExitWait
	t.Cleanup(func() { leaderExitWait = prev })
	leaderExitWait = func(int) bool { return false }
}

// TestDeadlineStopFailedExitWaitStillKills: a wait for the exit that fails
// keeps the gate open while git runs, so the deadline still sends its group
// kill (claustrum's own, not measured).
func TestDeadlineStopFailedExitWaitStillKills(t *testing.T) {
	kills := countGroupKills(t)
	failLeaderExitWait(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd, _ := stdinGit(t, ctx)
	gate := &leaderGate{}
	var cancelled atomic.Bool
	cmd.Cancel = deadlineStop(cmd, gate, &cancelled)
	var errBuf bytes.Buffer
	if err := runCheckoutPipes(cmd, &errBuf, func(error) bool { return false }, nil, gate); err == nil {
		t.Fatal("git hash-object ended well, want the kill of the deadline")
	}
	if n := kills(); n != 1 {
		t.Errorf("group kills = %d, want 1", n)
	}
}

// TestDeadlineStopFailedExitWaitAfterReap: with a wait for the exit that
// fails, the gate still closes before the reap of a git that exited by itself,
// so a late stop sends no group kill (claustrum's own, not measured).
func TestDeadlineStopFailedExitWaitAfterReap(t *testing.T) {
	kills := countGroupKills(t)
	failLeaderExitWait(t)
	cmd, feed := stdinGit(t, context.Background())
	gate := &leaderGate{}
	var cancelled atomic.Bool
	stop := deadlineStop(cmd, gate, &cancelled)
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = feed.Close()
	}()
	var errBuf bytes.Buffer
	if err := runCheckoutPipes(cmd, &errBuf, func(error) bool { return false }, nil, gate); err != nil {
		t.Fatalf("git hash-object = %v, %s", err, errBuf.String())
	}
	_ = stop()
	if n := kills(); n != 0 {
		t.Errorf("group kills after the reap = %d, want 0", n)
	}
}

// TestWorktreeCreateLateDeadlineStopSendsNoGroupKill: a create with a timeoutMs
// whose checkout git exits by itself. The test runs cmd.Cancel of the read-tree
// call right after its wait, as the watch of the context of Go can. That late
// stop sends no group kill, and the create succeeds (claustrum's own, from the
// code).
func TestWorktreeCreateLateDeadlineStopSendsNoGroupKill(t *testing.T) {
	kills := countGroupKills(t)
	var stops atomic.Int32
	prev := checkoutWaited
	t.Cleanup(func() { checkoutWaited = prev })
	checkoutWaited = func(c *exec.Cmd) {
		if c.Cancel != nil {
			stops.Add(1)
			_ = c.Cancel()
		}
	}
	f := newWTFixture(t, false)
	raw, _ := f.create(t, newTestGitDeps(), "w1", "", markerTimeoutMs)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("reply = %s, want success", raw)
	}
	if stops.Load() == 0 {
		t.Fatal("no read-tree call with a deadline stop ran")
	}
	if n := kills(); n != 0 {
		t.Errorf("group kills after the reap = %d, want 0", n)
	}
}
