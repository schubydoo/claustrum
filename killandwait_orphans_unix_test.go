//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// killAndWait with escalate:true must send the group SIGKILL at the end of the grace
// when the graceful signal already killed the managed process itself and its exit
// drain is still pending (89cb6289, Linux rows B3 and X1).
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
	// The "tree-stdout" fixture's grandchild inherits stdout, so it holds the
	// pipe open after the leader dies — which is what keeps the drain (and
	// p.done) pending. The test binary itself is the fixture, per AGENTS.md; a
	// /bin/sh script would put this test at the mercy of the platform shell's
	// job-control behavior.
	exe, env := helperCommand(t, "tree-stdout")

	m := newTestProcManager(t)
	c, _ := pipeConn(t)

	if _, err := m.spawn(c, "orphans", exe, []string{pidFile}, "", env, false); err != nil {
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

// spawnTreeWithKid spawns the "tree" helper as id and returns it with the pid of its
// kid. The kid is in the process group of the helper and has /dev/null as its stdio,
// so it holds no pipe of the helper. The helper ends on SIGTERM. The test ends the kid
// by its pid.
func spawnTreeWithKid(t *testing.T, m *procManager, id string, extraArgs ...string) (*managedProc, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "kid")
	exe, env := helperCommand(t, "tree")
	c, _ := pipeConn(t)
	p, err := m.spawn(c, id, exe, append([]string{pidFile}, extraArgs...), "", env, false)
	if err != nil {
		t.Fatalf("spawn %s: %v", id, err)
	}
	// The helper starts its kid after one byte on stdin.
	if !m.writeStdin(id, []byte("g")) {
		t.Fatalf("stdin of %s was not accepted", id)
	}
	kid := waitPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(kid, syscall.SIGKILL) })
	return p, kid
}

// groupKills records the names of the signals that go to p through the signalGroup
// seam, until the test ends. Every signal still goes out: p is a child of the test.
func groupKills(t *testing.T, p *managedProc) func() []string {
	t.Helper()
	realSignal := signalGroup
	var mu sync.Mutex
	var names []string
	signalGroup = func(g *procGroup, proc *os.Process, signame string) error {
		if proc == p.cmd.Process {
			mu.Lock()
			names = append(names, signame)
			mu.Unlock()
		}
		return realSignal(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = realSignal })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(names)
	}
}

// wantCleanExitLeavesTheKid fails if the group of the ended process got a SIGKILL, or
// if the kid is gone.
func wantCleanExitLeavesTheKid(t *testing.T, sent []string, kid int) {
	t.Helper()
	for _, name := range sent {
		if strings.Contains(name, "KILL") {
			t.Errorf("signals to the process = %q, want no KILL after its clean exit", sent)
		}
	}
	if len(sent) != 1 {
		t.Errorf("signals to the process = %q, want the one graceful signal", sent)
	}
	if waitProcessGone(kid, 300*time.Millisecond) {
		t.Errorf("the kid %d in the group of the process is gone, want it alive", kid)
	}
}

// TestKillAndWaitSendsNoGroupKillAfterACleanExit pins Linux row X2b (89cb6289, 3 of 3
// runs): process.killAndWait with the default sends SIGTERM to the one process. The
// process ends within the grace, and its group gets no SIGKILL. A kid in that group
// with its own stdio stays alive. The reply has no escalated member.
func TestKillAndWaitSendsNoGroupKillAfterACleanExit(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	p, kid := spawnTreeWithKid(t, m, "c1")
	sent := groupKills(t, p)

	found, died, alreadyExited, escalated := m.killAndWait("c1", "TERM", 10*time.Second, true)
	if !found || !died || alreadyExited || escalated {
		t.Errorf("killAndWait = found %v, died %v, alreadyExited %v, escalated %v, want true, true, false, false",
			found, died, alreadyExited, escalated)
	}
	wantCleanExitLeavesTheKid(t, sent(), kid)
}

// TestSupersedeSendsNoGroupKillAfterACleanExit pins Linux row X2a (89cb6289, 4 of 4
// runs): the session supersede sends SIGTERM to the one old process. That process ends
// within the grace, and its group gets no SIGKILL. A kid in that group with its own
// stdio stays alive, and the supersedes line says escalated=false.
func TestSupersedeSendsNoGroupKillAfterACleanExit(t *testing.T) {
	m := newTestProcManager(t)
	m.supersedeGrace = 10 * time.Second
	t.Cleanup(m.killAll)
	p, kid := spawnTreeWithKid(t, m, "s1", sessionArgs...)
	sent := groupKills(t, p)

	buf := captureLogBuf(t)
	c2, _ := pipeConn(t)
	cat, env := helperCommand(t, "cat")
	s2, err := m.spawn(c2, "s2", cat, sessionArgs, "", env, false)
	if err != nil {
		t.Fatalf("spawn s2: %v", err)
	}
	t.Cleanup(func() { endOwnChild(t, s2) })
	if want := "process s2 supersedes s1 for session SESS: died=true escalated=false alreadyExited=false"; !strings.Contains(buf.String(), want) {
		t.Errorf("log has no %q:\n%s", want, buf.String())
	}
	wantCleanExitLeavesTheKid(t, sent(), kid)
}

// killGroupAfterExit deliberately skips the reaped guard, so it is the one
// signal path with no liveness check in front of it. Its only remaining guard is
// against a process that was never started — reachable when a spawn failed
// before exec. It must be a silent no-op, not a nil dereference.
func TestKillGroupAfterExitIgnoresUnstartedProcess(t *testing.T) {
	_ = (&managedProc{id: "never-started"}).killGroupAfterExit()
	_ = (&managedProc{id: "no-os-process", cmd: &exec.Cmd{}}).killGroupAfterExit()
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
