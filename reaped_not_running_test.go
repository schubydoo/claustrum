package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Measured on reference build 90fca6e6: inside the exit drain, process.reattach
// reports running:false and process.stdin answers -32602. 19f30c46 reported
// running:true and accepted the write.
//
// claustrum's running and reaped flags differ only inside the exit drain: reaped
// is set the moment cmd.Wait returns, and running stays true until the exit frame
// goes out, which is up to exitDrainGrace later. See the reaped comment in
// internal/supervisor/process.go. So the window this pins is real and reachable.

// addReapedProc puts a process in the exit-drain window into the table of s: already
// reaped, still reporting running. AddFakeProcForTest wires the stdin condition
// variable because, without the fix, the stdin path reaches the enqueue and a nil
// condition variable would turn a clean assertion failure into a recovered panic.
func addReapedProc(s *server, id string) {
	s.procs.AddFakeProcForTest(id, true, true)
}

// TestReapedProcessRefusesStdin pins the stdin half. A reaped process has no pipe
// left to write to, so the reference answers the same "Process not running" error
// it gives an exited one. No new error string is involved.
func TestReapedProcessRefusesStdin(t *testing.T) {
	s := newTestServer(t)
	goodB64 := base64.StdEncoding.EncodeToString([]byte("x"))

	// The control that must keep passing: a live process is still accepted. It has
	// no stdin writer goroutine, so the bytes sit in the queue and are never
	// drained, but the reply is a success rather than the running error. This arm
	// fails a mutant that refuses every process.
	//
	// It stays a t.Fatalf and it stays FIRST. The new arm below is one-sided: on
	// its own it cannot tell "refuses a reaped process" from "refuses everything".
	// The control is the only thing making this test discriminate.
	s.procs.AddFakeProcForTest("live", true, false)
	if got := dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "live", "data": goodB64})); strings.Contains(got, "Process not running") {
		t.Fatalf("stdin to a live process = %s, want no running error", got)
	}

	// The new arm. Without the fix claustrum accepts the write.
	addReapedProc(s, "draining")
	got := dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "draining", "data": goodB64}))
	if !strings.Contains(got, "Process not running") {
		t.Errorf("stdin to a reaped process = %s, want the not-running error", got)
	}
}

// TestReapedProcessReattachReportsNotRunning pins the reattach half. The running
// field of the reattach result is what a client uses to decide whether the session
// is still alive, so a reaped process must not claim to be running.
func TestReapedProcessReattachReportsNotRunning(t *testing.T) {
	s := newTestServer(t)

	// The control: a live process still reports running true.
	s.procs.AddFakeProcForTest("live", true, false)
	if got := dispatchRaw(t, s, rpcLine(t, "process.reattach", map[string]any{"id": "live"})); !strings.Contains(got, `"running":true`) {
		t.Fatalf("reattach to a live process = %s, want running true", got)
	}

	// The new arm. Without the fix claustrum reports running true here.
	addReapedProc(s, "draining")
	got := dispatchRaw(t, s, rpcLine(t, "process.reattach", map[string]any{"id": "draining"}))
	if !strings.Contains(got, `"running":false`) {
		t.Errorf("reattach to a reaped process = %s, want running false", got)
	}
}
