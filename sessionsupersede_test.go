package main

import (
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

// endOwnChild ends a child of the test that no kill of the daemon reaches, and waits
// for its end. A child that outlives its test writes its exit line into the log
// capture of a later test.
func endOwnChild(t *testing.T, p *supervisor.Proc) {
	t.Helper()
	p.KillForTest()
	select {
	case <-p.DoneForTest():
	case <-time.After(10 * time.Second):
		t.Errorf("the child %s of the test did not end in 10 s", p.IDForTest())
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
