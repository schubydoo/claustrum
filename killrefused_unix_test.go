//go:build unix

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// refusingSignals makes every signal of a child fail with err. Both seams take the
// call, so no signal goes out: signalOne for TERM, INT and HUP, and killChildGroup
// for KILL. It returns the record of the calls, "one <sig>" or "group".
func refusingSignals(t *testing.T, err error) *[]string {
	t.Helper()
	oldOne, oldGroup := signalOne, killChildGroup
	t.Cleanup(func() { signalOne, killChildGroup = oldOne, oldGroup })
	var calls []string
	signalOne = func(_ *os.Process, sig syscall.Signal) error {
		calls = append(calls, "one "+sig.String())
		return err
	}
	killChildGroup = func(pgid int) error {
		calls = append(calls, "group")
		return err
	}
	return &calls
}

// refusedChild is a live record that no exit ends. Its pid is above every pid limit,
// so a signal that misses the seams finds no process.
func refusedChild(m *procManager, id string) *managedProc {
	p := &managedProc{
		id: id, running: true, group: &procGroup{},
		cmd: &exec.Cmd{Process: &os.Process{Pid: 1 << 30}}, done: make(chan struct{}),
	}
	m.procs[id] = p
	return p
}

// A signal that the system refuses answers -32603 with the text of the error, for
// TERM and for KILL (52d82f0a, measured on Linux and macOS with a child that runs as
// root). The kill stamps no killedBy, so a later exit frame has none.
func TestKillAnswersARefusedSignal(t *testing.T) {
	calls := refusingSignals(t, syscall.EPERM)
	s := newTestServer(t)
	p := refusedChild(s.procs, "k1")
	for _, sig := range []string{"TERM", "KILL"} {
		got := dispatchRaw(t, s, `{"jsonrpc":"2.0","id":4,"method":"process.kill","params":{"id":"k1","signal":"`+sig+`"},"auth":"`+testToken+`"}`)
		want := `{"jsonrpc":"2.0","id":4,"error":{"code":-32603,"message":"operation not permitted"}}`
		if got != want {
			t.Errorf("process.kill %s = %s, want %s", sig, got, want)
		}
	}
	if strings.Join(*calls, ",") != "one terminated,group" {
		t.Errorf("signal calls = %v, want the leader TERM and the group KILL", *calls)
	}
	if got := killedByOf(p); got != "" {
		t.Errorf("a refused kill stamped killedBy=%q, want empty", got)
	}
}

// A process that is done, and a pid or group that is gone, keep the success and the
// stamp of before (claustrum's own, not measured).
func TestKillOfAGoneProcessStaysASuccess(t *testing.T) {
	for _, err := range []error{os.ErrProcessDone, syscall.ESRCH} {
		refusingSignals(t, err)
		s := newTestServer(t)
		p := refusedChild(s.procs, "k1")
		got := dispatchRaw(t, s, `{"jsonrpc":"2.0","id":4,"method":"process.kill","params":{"id":"k1","signal":"KILL"},"auth":"`+testToken+`"}`)
		if want := `{"jsonrpc":"2.0","id":4,"result":{"success":true}}`; got != want {
			t.Errorf("process.kill with %v = %s, want %s", err, got, want)
		}
		if got := killedByOf(p); got != "client" {
			t.Errorf("a kill with %v stamped killedBy=%q, want client", err, got)
		}
	}
}

// killAndWait on a child that refuses each signal answers no error (52d82f0a,
// measured on Linux and macOS). It logs the refused first signal, then the refused
// group kill at the end of the wait, then the child that still runs after the
// reap grace. With escalate false it logs the first line only.
func TestKillAndWaitOfARefusedChild(t *testing.T) {
	refusingSignals(t, syscall.EPERM)
	oldGrace := killReapGrace
	killReapGrace = 20 * time.Millisecond
	t.Cleanup(func() { killReapGrace = oldGrace })

	for _, escalate := range []bool{true, false} {
		buf := captureLogBuf(t)
		m := newTestProcManager(t)
		p := refusedChild(m, "refused-k1")
		found, died, alreadyExited, escalated := m.killAndWait("refused-k1", "", 10*time.Millisecond, escalate)
		if !found || died || alreadyExited || escalated != escalate {
			t.Errorf("escalate %v: killAndWait = (found %v, died %v, alreadyExited %v, escalated %v), want (true, false, false, %v)",
				escalate, found, died, alreadyExited, escalated, escalate)
		}
		var want []string
		want = append(want, "[process.Manager] KillAndWait refused-k1: signal terminated failed: operation not permitted")
		if escalate {
			want = append(want,
				"[process.Manager] KillAndWait refused-k1: group kill failed: operation not permitted",
				"[process.Manager] KillAndWait refused-k1: still running 7s after SIGKILL (D-state?)")
		}
		var lines []string
		for _, l := range strings.Split(buf.String(), "\n") {
			if i := strings.Index(l, "[process.Manager] KillAndWait refused-k1:"); i >= 0 {
				lines = append(lines, l[i:])
			}
		}
		if strings.Join(lines, "\n") != strings.Join(want, "\n") {
			t.Errorf("escalate %v: log lines = %q, want %q", escalate, lines, want)
		}
		if got := killedByOf(p); got != "" {
			t.Errorf("escalate %v: a refused killAndWait stamped killedBy=%q, want empty", escalate, got)
		}
	}
}

// A refused signal leaves a pending farewell and its timer as they are, so a waiting
// killAndWait does not answer "cut" for a child that lives (claustrum's own, not
// measured).
func TestRefusedKillKeepsAPendingFarewell(t *testing.T) {
	refusingSignals(t, syscall.EPERM)
	m := newTestProcManager(t)
	p := refusedChild(m, "refused-f1")
	h := p.holdFarewell("TERM")
	if h == nil {
		t.Fatal("holdFarewell refused a live record")
	}
	if err := m.kill("refused-f1", "TERM"); err == nil {
		t.Fatal("the refused kill returned no error")
	}
	select {
	case <-h.cut:
		t.Error("a refused kill cut the pending farewell")
	default:
	}
	if !p.dropFarewell(h) {
		t.Error("the farewell is no longer pending after a refused kill")
	}
}

// A refused signal sets no signalled mark, so a later process.kill with a farewell
// still gets "queued" (claustrum's own, not measured).
func TestRefusedKillLeavesTheFarewellOpen(t *testing.T) {
	refusingSignals(t, syscall.EPERM)
	m := newTestProcManager(t)
	p := refusedChild(m, "refused-f2")
	// stdinDone makes the queue take the farewell bytes with no writer.
	p.stdinDone = true
	_ = m.kill("refused-f2", "TERM")
	got := m.killWithFarewell("refused-f2", "TERM", farewellAsk{data: []byte("bye"), grace: time.Hour})
	t.Cleanup(func() {
		p.mu.Lock()
		p.cutFarewellLocked()
		p.mu.Unlock()
	})
	if got != farewellQueued {
		t.Errorf("killWithFarewell after a refused kill = %q, want %q", got, farewellQueued)
	}
}
