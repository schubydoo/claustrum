package main

import (
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// supersedeSession with an empty key is a no-op and must never evict a process.
// Production only reaches supersedeSession for a non-empty session key (spawn gates
// the call), so this exercises the defensive empty-key guard directly. The victim's
// sessionKey is "" — the exact value that would match supersedeSession("") if the
// guard were absent — yet it must survive.
func TestSupersedeSessionEmptyKeyIsNoop(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	c, _ := pipeConn(t)
	cat, env := helperCommand(t, "cat")
	p, err := m.spawn(c, "victim", cat, nil, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	m.supersedeSession("", "other")
	// Without the guard the call collects the victim and sends it SIGTERM. cat ends
	// promptly on SIGTERM, so poll long enough for that kill to surface.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !p.isRunning() {
			t.Fatal(`supersedeSession("") killed a process; the empty-key guard did not fire`)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSupersedeSameIDAndSessionSendsNoSignal pins a rule of 89cb6289 (measured on Linux and macOS): a
// spawn with the id AND the session of a process that still runs supersedes nothing and sends no
// signal.
func TestSupersedeSameIDAndSessionSendsNoSignal(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	c1, _ := pipeConn(t)
	cat, env := helperCommand(t, "cat")
	first, err := m.spawn(c1, "s1", cat, sessionArgs, "", env, false)
	if err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	// No kill of the daemon reaches the first process after the replace, so the test
	// ends its own child.
	t.Cleanup(func() { endOwnChild(t, first) })
	signals := countSignalsTo(t, first)

	buf := captureLogBuf(t)
	c2, _ := pipeConn(t)
	second, err := m.spawn(c2, "s1", cat, sessionArgs, "", env, false)
	if err != nil {
		t.Fatalf("second spawn: %v", err)
	}
	t.Cleanup(func() { endOwnChild(t, second) })
	if n := signals.Load(); n != 0 {
		t.Errorf("the second spawn sent %d signal(s) to the first process, want none", n)
	}
	if !first.isLive() {
		t.Error("the first process ended")
	}
	if strings.Contains(buf.String(), "process s1 supersedes") {
		t.Errorf("a supersedes line was logged:\n%s", buf.String())
	}
}

// TestSessionSpawnOfAMissingCommandSupersedesNothing pins the pre-check of a session
// spawn against 89cb6289: a command that does not exist fails before the supersede.
// The prior process of the session gets no signal and lives on.
//
//   - Linux and macOS: a missing path answers "fork/exec <path>: no
//     such file or directory".
//   - Windows: a missing path that ends in .exe answers "fork/exec <path>:
//     The system cannot find the file specified."
//   - Windows: a missing path with no extension answers the lookup error.
//   - Linux and Windows: a bare name answers the lookup error.
func TestSessionSpawnOfAMissingCommandSupersedesNothing(t *testing.T) {
	type spawnCase struct {
		name, command string
		check         func(*testing.T, error)
	}
	lookupError := func(t *testing.T, err error) {
		if !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("error %v, want the lookup error", err)
		}
	}
	noExt := filepath.Join(t.TempDir(), "nocmd")
	missing, errnoText := noExt, "no such file or directory"
	if runtime.GOOS == "windows" {
		missing, errnoText = noExt+".exe", "The system cannot find the file specified."
	}
	cases := []spawnCase{
		{"path", missing, func(t *testing.T, err error) {
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("error %v, want a not-exist error", err)
			}
			if want := "fork/exec " + missing + ": " + errnoText; err.Error() != want {
				t.Errorf("error %q, want %q", err, want)
			}
		}},
		{"bare name", "claustrum-no-such-command", lookupError},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, spawnCase{"path with no extension", noExt, lookupError})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestProcManager(t)
			t.Cleanup(m.killAll)
			// A run dir, as under a daemon. On Linux and macOS the trampoline then
			// wraps the command, and the pre-check runs before that wrap.
			m.runDir = t.TempDir()
			c1, _ := pipeConn(t)
			cat, env := helperCommand(t, "cat")
			first, err := m.spawn(c1, "s1", cat, sessionArgs, "", env, false)
			if err != nil {
				t.Fatalf("first spawn: %v", err)
			}
			t.Cleanup(func() { endOwnChild(t, first) })
			signals := countSignalsTo(t, first)

			buf := captureLogBuf(t)
			c2, _ := pipeConn(t)
			_, err = m.spawn(c2, "s2", tc.command, sessionArgs, "", env, false)
			if err == nil {
				t.Fatal("the spawn of a missing command worked")
			}
			tc.check(t, err)
			if n := signals.Load(); n != 0 {
				t.Errorf("the failed spawn sent %d signal(s) to the first process, want none", n)
			}
			if !first.isLive() {
				t.Error("the first process ended")
			}
			logged := buf.String()
			if strings.Contains(logged, "process s2 supersedes") {
				t.Errorf("a supersedes line was logged:\n%s", logged)
			}
			if want := "[process.Manager] Failed to start process s2: " + err.Error(); !strings.Contains(logged, want) {
				t.Errorf("log has no %q:\n%s", want, logged)
			}
		})
	}
}
