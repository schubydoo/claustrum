//go:build unix

package supervisor

import (
	"strings"
	"syscall"
	"testing"
	"time"
)

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
		found, died, alreadyExited, escalated := m.KillAndWait("refused-k1", "", 10*time.Millisecond, escalate)
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
	if err := m.Kill("refused-f1", "TERM"); err == nil {
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
	_ = m.Kill("refused-f2", "TERM")
	got := m.KillWithFarewell("refused-f2", "TERM", FarewellAsk{data: []byte("bye"), grace: time.Hour})
	t.Cleanup(func() {
		p.mu.Lock()
		p.cutFarewellLocked()
		p.mu.Unlock()
	})
	if got != farewellQueued {
		t.Errorf("killWithFarewell after a refused kill = %q, want %q", got, farewellQueued)
	}
}
