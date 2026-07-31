//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// killAndWait with escalate:true must sweep up the process group even when the
// graceful signal already killed the managed process itself.
//
// This is the case that made the escalation a silent no-op. The child dies from
// SIGTERM immediately, but a grandchild still holding its stdout pipe keeps the
// exit drain pending, so p.done does not fire and the grace elapses. At that
// point the child is already reaped — and the reaped guard on the ordinary
// signal path would skip the SIGKILL entirely, leaving the grandchild running.
//
// Measured against the reference at 5db5e4a: killAndWait with escalate:true
// leaves no backgrounded sleeper alive, while escalate:false spares it.
func TestKillAndWaitEscalationReapsOrphanedGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "gpid")
	// The backgrounded sleeper inherits stdout, so it holds the pipe open after
	// the shell dies — which is what keeps the drain (and p.done) pending.
	script := "sleep 60 & echo $! > " + pidFile + "; sleep 60"

	m := newTestProcManager(t)
	c, _ := pipeConn(t)

	if _, err := m.spawn(c, "orphans", "/bin/sh", []string{"-c", script}, "", nil); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	gpid := waitPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(gpid, syscall.SIGKILL) })
	if err := syscall.Kill(gpid, 0); err != nil {
		t.Fatalf("grandchild %d not alive before killAndWait: %v", gpid, err)
	}

	// A short grace so the test does not sit through the production default.
	found, _, _, _ := m.killAndWait("orphans", "TERM", 300*time.Millisecond, true)
	if !found {
		t.Fatal("killAndWait did not find the process")
	}

	if !waitProcessGone(gpid, 5*time.Second) {
		t.Errorf("grandchild %d survived killAndWait(escalate:true) — the escalation "+
			"was skipped because the child was already reaped", gpid)
	}
}

// killGroupAfterExit deliberately skips the reaped guard, so it is the one
// signal path with no liveness check in front of it. Its only remaining guard is
// against a process that was never started — reachable when a spawn failed
// before exec. It must be a silent no-op, not a nil dereference.
func TestKillGroupAfterExitIgnoresUnstartedProcess(t *testing.T) {
	(&managedProc{id: "never-started"}).killGroupAfterExit()
	(&managedProc{id: "no-os-process", cmd: &exec.Cmd{}}).killGroupAfterExit()
}

// waitPIDFile polls until the file holds a parseable PID.
func waitPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				if pid, err := strconv.Atoi(s); err == nil {
					return pid
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid file %s never became readable", path)
	return 0
}
