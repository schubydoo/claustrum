package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// TestFailedCheckoutKillsLiveMember: git fails by itself with no timeoutMs, and
// its child in the group of git still runs. The group gets one kill before the
// reap of git, and it ends the child, as 4c111e86 ends it (measured on Linux).
func TestFailedCheckoutKillsLiveMember(t *testing.T) {
	all, beforeReap := groupKillsAtReap(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd, _, held := lingeringReadTree(t, ctx, "fail", nil)
	var errBuf bytes.Buffer
	var exitErr *exec.ExitError
	if err := runCheckoutPipes(cmd, &errBuf, noSkip, nil, nil, nil); !errors.As(err, &exitErr) {
		t.Fatalf("read-tree = %v, want the exit error of git", err)
	}
	if n, b := all.Load(), beforeReap.Load(); n != 1 || b != 1 {
		t.Errorf("group kills = %d, before the reap = %d, want 1 and 1", n, b)
	}
	if !endsWithin(held, 10*time.Second) {
		t.Error("the child of git still runs 10 s after the reply: the group kill did not reach it")
	}
}

// TestFailedCheckoutWithNoMemberKillsOnce: git fails by itself and leaves no
// child. The group still gets its one kill before the reap, and only the exited
// git is in that group (from the code).
func TestFailedCheckoutWithNoMemberKillsOnce(t *testing.T) {
	all, beforeReap := groupKillsAtReap(t, true)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "stderr-exit5"})
	cmd.SysProcAttr = sysproc.NewSysProcAttr()
	cmd.WaitDelay = time.Second
	var errBuf bytes.Buffer
	var exitErr *exec.ExitError
	if err := runCheckoutPipes(cmd, &errBuf, noSkip, nil, nil, nil); !errors.As(err, &exitErr) || exitErr.ExitCode() != 5 {
		t.Fatalf("read-tree = %v, want exit status 5", err)
	}
	if n, b := all.Load(), beforeReap.Load(); n != 1 || b != 1 {
		t.Errorf("group kills = %d, before the reap = %d, want 1 and 1", n, b)
	}
}

// TestFailedCheckoutUnseenExitSendsNothing: the wait for the exit of a failed
// git does not see the exit. Then the reap can come first, so no group kill goes
// (claustrum's own, from the code).
func TestFailedCheckoutUnseenExitSendsNothing(t *testing.T) {
	all, _ := groupKillsAtReap(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd, _, _ := lingeringReadTree(t, ctx, "fail", nil)
	seenWait := leaderExitWait
	t.Cleanup(func() { leaderExitWait = seenWait })
	leaderExitWait = func(pid int) (bool, bool) {
		seenWait(pid)
		return false, false
	}
	var errBuf bytes.Buffer
	var exitErr *exec.ExitError
	if err := runCheckoutPipes(cmd, &errBuf, noSkip, nil, nil, nil); !errors.As(err, &exitErr) {
		t.Fatalf("read-tree = %v, want the exit error of git", err)
	}
	if n := all.Load(); n != 0 {
		t.Errorf("group kills = %d, want 0", n)
	}
}

// TestFailedCheckoutAfterStopSendsNoSecondKill: the deadline stop runs after
// the failed exit of git and before the gate closes. Its group kill is the only
// one (claustrum's own, from the code).
func TestFailedCheckoutAfterStopSendsNoSecondKill(t *testing.T) {
	all, _ := groupKillsAtReap(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd, st, _ := lingeringReadTree(t, ctx, "fail", func(c *exec.Cmd) { _ = c.Cancel() })
	var errBuf bytes.Buffer
	var exitErr *exec.ExitError
	if err := runCheckoutPipes(cmd, &errBuf, noSkip, nil, st, nil); !errors.As(err, &exitErr) {
		t.Fatalf("read-tree = %v, want the exit error of git", err)
	}
	if n := all.Load(); n != 1 {
		t.Errorf("group kills = %d, want 1: the one of the stop", n)
	}
}

// TestWorktreeCreateFailedCheckoutEndsGroupMember: a create with no timeoutMs
// whose read-tree git fails while its child, in the group of git, holds stdout
// for 20 s. The group kill ends the child, so the reply comes before the drain
// of gitDrainWait (4c111e86 ends such a child, measured on Linux).
func TestWorktreeCreateFailedCheckoutEndsGroupMember(t *testing.T) {
	// The cost of the create with a child that holds nothing is the baseline, so a
	// slow runner's helper starts do not count against the drain bound.
	stubLingeringGit(t, false, "0")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT0", "fail")
	_, base := runLingeringCreate(t, t.TempDir(), 0)
	t.Setenv("CLAUSTRUM_GITSTUB_ORPHAN", "20")
	endHeldDescendant(t)
	raw, elapsed := runLingeringCreate(t, t.TempDir(), 0)
	if !strings.Contains(raw, failedFilterReply) || !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
		t.Fatalf("reply = %s, want the failure of git with worktree_add_failed", raw)
	}
	if limit := base + gitDrainWait/2; elapsed >= limit {
		t.Errorf("reply after %s, want under %s (baseline %s plus half of %s): the child of git still held stdout", elapsed, limit, base, gitDrainWait)
	}
	b, err := os.ReadFile(os.Getenv("CLAUSTRUM_GITSTUB_HOLDPID"))
	if err != nil {
		t.Fatalf("pid of the child of git: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid of the child of git: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !processEnded(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the child %d of git still runs 5 s after the reply", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// processEnded reports that pid is gone or a zombie, from /proc/<pid>/stat.
func processEnded(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || i+2 >= len(s) || s[i+2] == 'Z' || s[i+2] == 'X'
}
