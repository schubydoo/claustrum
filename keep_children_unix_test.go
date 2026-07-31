//go:build unix

package main

import (
	"syscall"
	"testing"
	"time"
)

// pidAlive reports whether an OS pid still exists (signal 0 probes existence
// without delivering anything). It lets the -keep-children tests assert a child
// genuinely survives — or doesn't — the shutdown decision, independent of our own
// bookkeeping.
func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// On POSIX, -keep-children is honored verbatim.
func TestHonorKeepChildrenUnix(t *testing.T) {
	if !honorKeepChildren(true) {
		t.Error("honorKeepChildren(true) = false, want true on POSIX")
	}
	if honorKeepChildren(false) {
		t.Error("honorKeepChildren(false) = true, want false")
	}
}

// With -keep-children set, graceful shutdown must leave a running child alive. We
// drive stopChildren (the exact step teardown runs) rather than teardown itself,
// which calls os.Exit and would take the test process down with it.
func TestStopChildrenKeepsChildAlive(t *testing.T) {
	m := newTestProcManager(t)
	c, frames := pipeConn(t)
	sleep, env := helperCommand(t, "sleep")
	if _, err := m.spawn(c, "keep", sleep, []string{"60"}, "", env); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	pid := m.get("keep").pid
	if pid <= 0 {
		t.Fatalf("spawned child has no pid (%d)", pid)
	}
	t.Cleanup(m.killAll) // never leak the 60s sleeper

	s := &server{procs: m, keepChildren: true}
	s.stopChildren()

	// The child must not have been signalled: no exit frame, and the OS pid is
	// still alive.
	select {
	case f := <-frames:
		if f.Stream == "exit" {
			t.Fatalf("child exited despite -keep-children (exit frame seq=%d)", f.Seq)
		}
	case <-time.After(300 * time.Millisecond):
	}
	if !pidAlive(pid) {
		t.Errorf("child pid %d is not alive after -keep-children shutdown", pid)
	}
}

// Without the flag, graceful shutdown kills the child (today's behavior, unchanged).
func TestStopChildrenKillsChildByDefault(t *testing.T) {
	m := newTestProcManager(t)
	c, frames := pipeConn(t)
	sleep, env := helperCommand(t, "sleep")
	if _, err := m.spawn(c, "kill", sleep, []string{"60"}, "", env); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	pid := m.get("kill").pid

	s := &server{procs: m, keepChildren: false}
	s.stopChildren()

	// killAll signalled the group → the child exits (its exit frame is emitted
	// after cmd.Wait reaps it, so the pid is gone by then).
	deadline := time.After(4 * time.Second)
	for exited := false; !exited; {
		select {
		case f := <-frames:
			if f.Stream == "exit" {
				exited = true
			}
		case <-deadline:
			t.Fatal("default shutdown did not kill the child (no exit frame)")
		}
	}
	if pidAlive(pid) {
		t.Errorf("child pid %d is still alive after default shutdown", pid)
	}
}
