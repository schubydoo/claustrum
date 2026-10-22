package main

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestKillAndWaitKillInsideDrainKillsGroupAtOnce pins cells K2a and K1a of rounds
// tier3-03 and tier3-12 (5fd08069, macOS and Linux VMs). Inside the exit drain
// "signal":"KILL" kills the group at once and answers died with no escalation.
// The default signal sends nothing and escalates at the end of the grace. The fake
// kill ends the drain.
func TestKillAndWaitKillInsideDrainKillsGroupAtOnce(t *testing.T) {
	oldSignal := signalGroup
	t.Cleanup(func() { signalGroup = oldSignal })
	var mu sync.Mutex
	var signals []string
	var done chan struct{}
	signalGroup = func(_ *procGroup, _ *os.Process, name string) error {
		mu.Lock()
		defer mu.Unlock()
		signals = append(signals, name)
		if name == "KILL" {
			close(done)
		}
		return nil
	}
	m := newTestProcManager(t)

	for _, tc := range []struct {
		signal    string
		grace     time.Duration
		escalated bool
	}{
		{"KILL", 10 * time.Second, false},
		{"", 20 * time.Millisecond, true},
	} {
		mu.Lock()
		signals = nil
		done = make(chan struct{})
		mu.Unlock()
		p := &managedProc{
			id: "DRAIN", running: true, reaped: true,
			cmd: &exec.Cmd{Process: &os.Process{Pid: 1}}, done: done,
		}
		start := time.Now()
		found, died, alreadyExited, escalated := m.killAndWaitProc(p, tc.signal, tc.grace, true)
		if !found || !died || alreadyExited || escalated != tc.escalated {
			t.Errorf("signal %q: (found %v, died %v, alreadyExited %v, escalated %v), want (true, true, false, %v)",
				tc.signal, found, died, alreadyExited, escalated, tc.escalated)
		}
		if !tc.escalated && time.Since(start) >= tc.grace {
			t.Errorf("signal %q: answered after %v, want before the grace %v", tc.signal, time.Since(start), tc.grace)
		}
		mu.Lock()
		got := strings.Join(signals, ",")
		mu.Unlock()
		if got != "KILL" {
			t.Errorf("signal %q: group signals %q, want one KILL", tc.signal, got)
		}
	}
}
