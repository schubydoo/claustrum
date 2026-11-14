//go:build linux || darwin

package supervisor

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// startHelperChild starts this test binary in the helper mode, in a group of its own
// when ownGroup is true. The test waits for the child itself.
func startHelperChild(t *testing.T, mode string, ownGroup bool, args ...string) *exec.Cmd {
	t.Helper()
	exe, env := helperCommand(t, mode)
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "CLAUSTRUM_TEST_HELPER="+env["CLAUSTRUM_TEST_HELPER"])
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: ownGroup}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// exitSeenWithin runs exitSeenUnreaped for pid and fails the test if it does not
// answer within the limit.
func exitSeenWithin(t *testing.T, pid int) bool {
	t.Helper()
	done := make(chan bool, 1)
	go func() { done <- exitSeenUnreaped(pid) }()
	select {
	case seen := <-done:
		return seen
	case <-time.After(10 * time.Second):
		t.Fatal("exitSeenUnreaped did not answer within 10 s")
		return false
	}
}

// TestExitSeenUnreapedLeavesTheChildForItsWait: the wait sees the exit of a child,
// also a second time after the exit, and the child stays unreaped in its own group
// until its own wait (claustrum's own, decision-099).
func TestExitSeenUnreapedLeavesTheChildForItsWait(t *testing.T) {
	cmd := startHelperChild(t, "echo", true)
	pid := cmd.Process.Pid
	if !exitSeenWithin(t, pid) {
		t.Fatal("the exit of the child was not seen")
	}
	if !exitSeenWithin(t, pid) {
		t.Error("the exit of a child that exited already was not seen")
	}
	if !groupMadeAtSpawn(pid) {
		t.Error("the exited child is not seen in the group that the spawn made")
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("the wait of the child: %v, want its exit status 0", err)
	}
}

// TestGroupMadeAtSpawnNeedsTheOwnGroup: a child in the group of the test is not in a
// group that the spawn made.
func TestGroupMadeAtSpawnNeedsTheOwnGroup(t *testing.T) {
	for _, own := range []bool{false, true} {
		cmd := startHelperChild(t, "sleep", own, "30")
		got := groupMadeAtSpawn(cmd.Process.Pid)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if got != own {
			t.Errorf("own group %v: groupMadeAtSpawn %v", own, got)
		}
	}
}
