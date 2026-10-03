package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sessionArgs are the argv tail that makes a spawn a stream-json session of SESS.
var sessionArgs = []string{"--output-format", "stream-json", "--session-id", "SESS"}

// endOwnChild ends a child of the test that no kill of the daemon reaches, and waits
// for its end. A child that outlives its test writes its exit line into the log
// capture of a later test.
func endOwnChild(t *testing.T, p *managedProc) {
	t.Helper()
	p.signalIfLive("KILL", "")
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Errorf("the child %s of the test did not end in 10 s", p.id)
	}
}

// countSignalsTo counts the calls of the signalGroup seam for the process p, until the
// test ends. A late signal to a child of an earlier test does not count.
func countSignalsTo(t *testing.T, p *managedProc) *atomic.Int32 {
	t.Helper()
	realSignal := signalGroup
	var signals atomic.Int32
	signalGroup = func(g *procGroup, proc *os.Process, signame string) error {
		if proc == p.cmd.Process {
			signals.Add(1)
		}
		return realSignal(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = realSignal })
	return &signals
}

// cliSessionKey gates superseding: a non-empty key needs stream-json mode AND a
// valid session id (session-id wins; resume is the fallback unless --fork-session).
func TestCliSessionKey(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"session-id equals form", []string{"--input-format=stream-json", "--session-id=abc"}, "abc"},
		{"session-id space form", []string{"--input-format=stream-json", "--session-id", "abc"}, "abc"},
		{"output-format stream-json + resume", []string{"--output-format=stream-json", "--resume=r1"}, "r1"},
		{"resume space form", []string{"--output-format=stream-json", "--resume", "r2"}, "r2"},
		{"bare stream-json token", []string{"stream-json", "--session-id=xy"}, "xy"},
		{"resume suppressed by fork-session", []string{"--input-format=stream-json", "--resume=r1", "--fork-session"}, ""},
		{"session-id wins over fork-session", []string{"--input-format=stream-json", "--session-id=s1", "--fork-session"}, "s1"},
		{"session-id beats resume", []string{"--input-format=stream-json", "--resume=r1", "--session-id=s1"}, "s1"},
		{"not stream-json", []string{"--session-id=abc"}, ""},
		{"stream-json no id", []string{"--input-format=stream-json"}, ""},
		{"session-id is last arg, no value", []string{"--input-format=stream-json", "--session-id"}, ""},
		{"next arg is a flag, rejected", []string{"--input-format=stream-json", "--session-id", "--resume"}, ""},
		{"token too long", []string{"--input-format=stream-json", "--session-id=" + strings.Repeat("a", 129)}, ""},
		{"token bad char", []string{"--input-format=stream-json", "--session-id=has space"}, ""},
		{"token with allowed punctuation", []string{"--input-format=stream-json", "--session-id=a-b_c.d:1"}, "a-b_c.d:1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cliSessionKey(tc.args); got != tc.want {
				t.Errorf("cliSessionKey(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

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

// A new stream-json session spawn supersedes the prior process of the same session
// (its exit frame reaches the client); a spawn of a DIFFERENT session does not.
// The session args are ignored by the cat helper but read by cliSessionKey; each
// cat stays alive on its stdin until superseded.
func TestSocketSupersedeSameSession(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)

	// s1 and s2 share SESS1 → spawning s2 supersedes s1.
	cl.send(spawnReqArgs(t, 1, "s1", "cat", "--input-format=stream-json", "--session-id=SESS1"))
	cl.waitResponses(1)
	cl.send(spawnReqArgs(t, 2, "s2", "cat", "--input-format=stream-json", "--session-id=SESS1"))
	if exit := lastExit(t, cl.waitExit("s1")); exit.ExitCode == nil || *exit.ExitCode == 0 {
		t.Errorf("superseded s1 exit code = %v, want a killed (non-zero) exit", exit.ExitCode)
	}
	cl.waitResponses(2) // drain the s2 spawn reply before the reattach round-trip

	// The superseding process s2 must SURVIVE its own spawn — supersede excludes the
	// new proc (the id != newID guard). Without it, s2 would kill itself too.
	var ra reattachResult
	decodeReply(t, cl.call(authed(`{"jsonrpc":"2.0","id":90,"method":"process.reattach","params":{"id":"s2","fromSeq":0}}`)), &ra)
	if !ra.Found || !ra.Running {
		t.Fatalf("superseding process s2 did not survive its own spawn (id != newID guard): found=%v running=%v", ra.Found, ra.Running)
	}

	// s3 is a DIFFERENT session (OTHER); it must NOT supersede s2.
	cl.send(spawnReqArgs(t, 3, "s3", "cat", "--input-format=stream-json", "--session-id=OTHER"))

	// s4 shares SESS1 → supersedes s2. That s2 is still alive to be superseded here
	// proves s3 (a different session) did not kill it.
	cl.send(spawnReqArgs(t, 4, "s4", "cat", "--input-format=stream-json", "--session-id=SESS1"))
	if exit := lastExit(t, cl.waitExit("s2")); exit.ExitCode == nil || *exit.ExitCode == 0 {
		t.Errorf("s2 exit code = %v, want a killed exit from the SESS1 supersede (proving s3 left it alive)", exit.ExitCode)
	}
	// The reply of s4 comes after the end of s2. Wait for it, so the spawn of s4 is
	// done before the cleanup of the test ends the children.
	cl.waitResponses(5)
}

// TestSupersedeSameIDAndSessionSendsNoSignal pins row B4 (89cb6289, Linux and macOS): a
// spawn with the id AND the session of a process that still runs supersedes nothing
// and sends no signal.
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
//   - Linux rows P3 and H1, macOS row P3: a missing path answers "fork/exec <path>: no
//     such file or directory".
//   - Windows row SSm-path: a missing path that ends in .exe answers "fork/exec <path>:
//     The system cannot find the file specified."
//   - Windows row SSm-noext: a missing path with no extension answers the lookup error.
//   - Linux row H9 and Windows row SSm-bare: a bare name answers the lookup error.
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
