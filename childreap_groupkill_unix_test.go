//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// TestKillGroupReachesEveryMemberOfTheGroup pins that procinfo.KillGroup signals the whole
// process group, not the leader alone. The leader is the "tree-stdout" helper, started
// with Setpgid so that its pid is the id of a new group. Its kid does not set a group,
// so it joins the group of the leader. A procinfo.KillGroup that signals only the pid ends the
// leader and leaves the kid running. The group holds only the two helpers that this
// test started, so the SIGKILL reaches nothing else.
func TestKillGroupReachesEveryMemberOfTheGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "kid")
	exe, env := helperCommand(t, "tree-stdout")
	cmd := exec.Command(exe, pidFile)
	cmd.Env = buildEnv(env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("leader helper: %v", err)
	}
	pgid := cmd.Process.Pid
	// One goroutine reaps the leader, so the test can bound that wait and the cleanup
	// can still wait for it.
	reaped := make(chan *os.ProcessState, 1)
	go func() {
		st, _ := cmd.Process.Wait()
		reaped <- st
	}()
	leaderReaped, groupGone := false, false
	t.Cleanup(func() {
		// An unreaped leader keeps pgid as the id of this group, and after its reap a
		// live kid does. So this signal reaches only the helpers of this test.
		if !groupGone && pgid > 1 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		if !leaderReaped {
			<-reaped
		}
	})
	if pgid <= 1 {
		t.Fatalf("leader pid %d is not a group this test may signal", pgid)
	}

	kid := waitPIDFile(t, pidFile)
	if kid <= 1 || kid == pgid {
		t.Fatalf("kid pid %d is not a second member this test may watch", kid)
	}
	if got, err := syscall.Getpgid(kid); err != nil || got != pgid {
		t.Fatalf("Getpgid(kid %d) = %d, %v, want the group %d of the leader", kid, got, err, pgid)
	}

	if err := procinfo.KillGroup(pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("procinfo.KillGroup(%d, SIGKILL) = %v", pgid, err)
	}
	var st *os.ProcessState
	select {
	case st = <-reaped:
		leaderReaped = true
	case <-time.After(5 * time.Second):
		t.Fatalf("leader %d did not end within 5s of procinfo.KillGroup(%d, SIGKILL)", pgid, pgid)
	}
	if st == nil {
		t.Fatalf("waiting for the killed leader %d failed", pgid)
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("leader %d ended with %v, want death by SIGKILL", pgid, st)
	}

	// The kid is a child of the leader. Once the leader is reaped, init or a subreaper
	// reaps the dead kid, and signal 0 on its pid then fails.
	if !waitProcessGone(kid, 5*time.Second) {
		t.Fatalf("kid %d of group %d is still alive after procinfo.KillGroup(SIGKILL): the signal reached the leader only", kid, pgid)
	}
	groupGone = true
}
