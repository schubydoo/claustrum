package main

import (
	"encoding/base64"
	"strings"
	"sync"
	"testing"
)

// Reference build 90fca6e6 NARROWED the "is this process running" test that
// process.reattach and process.stdin both consult. Before, it read the running
// flag alone. Now it reads running AND NOT reaped, so strictly fewer processes
// pass it.
//
// The two flags differ only inside the exit drain: reaped is set the moment
// cmd.Wait returns, and running stays true until the exit frame goes out, which is
// up to exitDrainGrace later. See the reaped comment in process.go. So the window
// this pins is real and reachable, and inside it the reference now reports the
// process as not running and refuses stdin.

// reapedProc builds a process in the exit-drain window: already reaped, still
// reporting running. The stdin condition variable is wired because, without the
// fix, the stdin path reaches the enqueue and a nil condition variable would turn a
// clean assertion failure into a recovered panic.
func reapedProc(id string) *managedProc {
	p := &managedProc{id: id, subs: map[*conn]struct{}{}, running: true, reaped: true}
	p.stdinCond = sync.NewCond(&p.stdinMu)
	return p
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
	live := &managedProc{id: "live", subs: map[*conn]struct{}{}, running: true}
	live.stdinCond = sync.NewCond(&live.stdinMu)
	s.procs.procs["live"] = live
	if got := dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "live", "data": goodB64})); strings.Contains(got, "Process not running") {
		t.Fatalf("stdin to a live process = %s, want no running error", got)
	}

	// The new arm. Without the fix claustrum accepts the write.
	s.procs.procs["draining"] = reapedProc("draining")
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

	// The control: a live process still reports running true. It gets a condition
	// variable it does not need here, so the literal stays safe to copy into a test
	// that does reach the stdin enqueue.
	liveProc := &managedProc{id: "live", subs: map[*conn]struct{}{}, running: true}
	liveProc.stdinCond = sync.NewCond(&liveProc.stdinMu)
	s.procs.procs["live"] = liveProc
	if got := dispatchRaw(t, s, rpcLine(t, "process.reattach", map[string]any{"id": "live"})); !strings.Contains(got, `"running":true`) {
		t.Fatalf("reattach to a live process = %s, want running true", got)
	}

	// The new arm. Without the fix claustrum reports running true here.
	s.procs.procs["draining"] = reapedProc("draining")
	got := dispatchRaw(t, s, rpcLine(t, "process.reattach", map[string]any{"id": "draining"}))
	if !strings.Contains(got, `"running":false`) {
		t.Errorf("reattach to a reaped process = %s, want running false", got)
	}
}
