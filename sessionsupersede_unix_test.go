//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// supersedeRun spawns the helper mode victim as s1, waits until it is ready, then
// spawns s2 in the same session with the grace that the caller gives. It returns the
// victim, the time that the second spawn took, and the log of that spawn.
func supersedeRun(t *testing.T, victimMode string, grace time.Duration) (victim *managedProc, took time.Duration, logged string) {
	t.Helper()
	m := newTestProcManager(t)
	m.supersedeGrace = grace
	t.Cleanup(m.killAll)

	c1, frames := pipeConn(t)
	exe, env := helperCommand(t, victimMode)
	victim, err := m.spawn(c1, "s1", exe, sessionArgs, "", env, false)
	if err != nil {
		t.Fatalf("spawn s1: %v", err)
	}
	if got := firstStdout(t, frames); got != "ready" {
		t.Fatalf("victim printed %q, want ready", got)
	}
	// Keep the frame reader of c1 moving, so the exit frame of the victim never
	// waits on a full channel.
	go func() {
		for range frames {
		}
	}()

	buf := captureLogBuf(t)
	c2, _ := pipeConn(t)
	cat, catEnv := helperCommand(t, "cat")
	start := time.Now()
	if _, err := m.spawn(c2, "s2", cat, sessionArgs, "", catEnv, false); err != nil {
		t.Fatalf("spawn s2: %v", err)
	}
	took = time.Since(start)
	if victim.isRunning() {
		t.Error("the spawn of s2 returned while the victim s1 still runs")
	}
	return victim, took, buf.String()
}

// wantLogOrder fails unless the parts come in this order in logged.
func wantLogOrder(t *testing.T, logged string, parts ...string) {
	t.Helper()
	at := 0
	for _, part := range parts {
		i := strings.Index(logged[at:], part)
		if i < 0 {
			t.Fatalf("log has no %q after place %d:\n%s", part, at, logged)
		}
		at += i + len(part)
	}
}

// TestSupersedeWaitsForAVictimThatExitsOnTERM pins row B1 (89cb6289, Linux and macOS):
// the victim exits on SIGTERM, and the spawn of the new process returns after that
// end, with the exit line, then the supersedes line, then the started line. The grace
// is 10 s here, so a slow runner does not reach it. The escalated=false of the
// supersedes line is the proof that the victim ended on its SIGTERM.
func TestSupersedeWaitsForAVictimThatExitsOnTERM(t *testing.T) {
	const grace = 10 * time.Second
	_, took, logged := supersedeRun(t, "term-exit0", grace)
	if took >= grace {
		t.Errorf("the spawn took %v, want less than the grace of %v", took, grace)
	}
	wantLogOrder(t, logged,
		"[process.Manager] Process s1 exited with code 0, signalled at client request",
		"[process.Manager] process s2 supersedes s1 for session SESS: died=true escalated=false alreadyExited=false",
		"[process.Manager] Process s2 started, PID=",
	)
}

// TestSupersedeWaitsForAVictimThatIgnoresTERM pins row B2: the victim ignores
// SIGTERM, gets SIGKILL after the grace, and the spawn of the new process returns
// only then.
func TestSupersedeWaitsForAVictimThatIgnoresTERM(t *testing.T) {
	const grace = 300 * time.Millisecond
	_, took, logged := supersedeRun(t, "ignore-term", grace)
	if took < grace {
		t.Errorf("the spawn took %v, want the grace of %v or more", took, grace)
	}
	wantLogOrder(t, logged,
		"[process.Manager] Process s1 exited with code -1, terminated by SIGKILL, signalled at client request",
		"[process.Manager] process s2 supersedes s1 for session SESS: died=true escalated=true alreadyExited=false",
		"[process.Manager] Process s2 started, PID=",
	)
}

// TestSessionSpawnOfANonExecutableCommandSupersedesFirst pins Linux rows H4a to H4c
// (89cb6289) for a command that exists and does not start: the pre-check of the
// spawn tests existence only, so the supersede runs and the error comes after it.
func TestSessionSpawnOfANonExecutableCommandSupersedesFirst(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	c1, _ := pipeConn(t)
	cat, env := helperCommand(t, "cat")
	first, err := m.spawn(c1, "s1", cat, sessionArgs, "", env, false)
	if err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	buf := captureLogBuf(t)
	c2, _ := pipeConn(t)
	_, err = m.spawn(c2, "s2", plain, sessionArgs, "", env, false)
	if want := "fork/exec " + plain + ": permission denied"; err == nil || err.Error() != want {
		t.Fatalf("error %v, want %q", err, want)
	}
	if first.isRunning() {
		t.Error("the first process still runs after the spawn")
	}
	wantLogOrder(t, buf.String(),
		"[process.Manager] process s2 supersedes s1 for session SESS: died=true",
		"[process.Manager] Failed to start process s2: fork/exec "+plain+": permission denied",
	)
}
