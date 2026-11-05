package main

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/supervisor"
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
	signalGroup = func(g *supervisor.ProcGroup, proc *os.Process, signame string) error {
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
		{"value of resume is not read as a flag", []string{"--input-format=stream-json", "--resume", "--session-id=abc"}, ""},
		{"value of session-id is not the stream-json mark", []string{"--session-id", "stream-json"}, ""},
		{"value of resume is not the fork mark", []string{"--input-format=stream-json", "--resume", "--fork-session", "--resume=r1"}, "r1"},
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
	var ra rpc.ReattachResult
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
