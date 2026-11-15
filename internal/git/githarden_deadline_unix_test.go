//go:build linux || darwin

package git

import (
	"bytes"
	"context"
	"os"
	"os/exec"
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
// still sends one group kill, before the reap.
func TestDeadlineStopWhileGitRunsKillsGroup(t *testing.T) {
	kills := countGroupKills(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
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
