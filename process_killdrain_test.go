package main

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/supervisor"
)

// TestKillAndWaitKillInsideDrainKillsGroupAtOnce pins measured cases (`5fd08069`,
// measured on Linux and macOS). Inside the exit drain "signal":"KILL" kills the
// group at once and answers died with no escalation. The default signal sends
// nothing and escalates at the end of the grace. The fake kill ends the drain.
func TestKillAndWaitKillInsideDrainKillsGroupAtOnce(t *testing.T) {
	oldSignal := signalGroup
	t.Cleanup(func() { signalGroup = oldSignal })
	var mu sync.Mutex
	var signals []string
	var done chan struct{}
	signalGroup = func(_ *supervisor.ProcGroup, _ *os.Process, name string) error {
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
			cmd: &exec.Cmd{Process: &os.Process{Pid: noSuchPid}}, done: done,
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

// TestKillInsideDrainKillsGroupAtOnce pins a measured case (`b3081b51`, measured on
// Linux and macOS). Inside the exit drain a process.kill with "signal":"KILL" kills
// the group at once and stamps no killedBy. TERM sends nothing there, and after the
// exit frame KILL sends nothing either. Windows sends nothing (claustrum's own, not
// measured). The seam records each signal.
func TestKillInsideDrainKillsGroupAtOnce(t *testing.T) {
	oldSignal := signalGroup
	t.Cleanup(func() { signalGroup = oldSignal })
	oldInUse := supervisor.SetPidInUseForTest(func(int) bool { return false })
	t.Cleanup(func() { supervisor.SetPidInUseForTest(oldInUse) })
	var mu sync.Mutex
	var signals []string
	signalGroup = func(_ *supervisor.ProcGroup, _ *os.Process, name string) error {
		mu.Lock()
		defer mu.Unlock()
		signals = append(signals, name)
		return nil
	}
	m := newTestProcManager(t)

	wantKill := ""
	if supervisor.KillEndsDrainGroup {
		wantKill = "KILL"
	}
	for _, tc := range []struct {
		signal  string
		running bool
		want    string
	}{
		{"KILL", true, wantKill},
		{"SIGKILL", true, wantKill},
		{"TERM", true, ""},
		{"KILL", false, ""},
	} {
		mu.Lock()
		signals = nil
		mu.Unlock()
		// The pid is above every pid_max, so a signal that passes the seam finds no process.
		p := &managedProc{
			id: "DRAIN", running: tc.running, reaped: true,
			cmd: &exec.Cmd{Process: &os.Process{Pid: 1 << 30}}, done: make(chan struct{}),
		}
		m.procs["DRAIN"] = p
		if err := m.kill("DRAIN", tc.signal); err != nil {
			t.Errorf("signal %q, running %v: kill error %v, want nil", tc.signal, tc.running, err)
		}
		mu.Lock()
		got := strings.Join(signals, ",")
		mu.Unlock()
		if got != tc.want {
			t.Errorf("signal %q, running %v: group signals %q, want %q", tc.signal, tc.running, got, tc.want)
		}
		if by := killedByOf(p); by != "" {
			t.Errorf("signal %q, running %v: killedBy %q, want none", tc.signal, tc.running, by)
		}
	}
}

// TestKillInsideDrainSkipsGroupKillAtALivePid pins the narrowing of the exit-drain kill
// (claustrum's own, not measured). If pidInUse finds a live process at the pid of the
// ended child, process.kill sends no group KILL. If it finds none (ESRCH), the KILL goes.
// killAndWait reads no pidInUse and kills the group in both cases. Both seams are stubs.
func TestKillInsideDrainSkipsGroupKillAtALivePid(t *testing.T) {
	oldSignal := signalGroup
	t.Cleanup(func() { signalGroup = oldSignal })
	var mu sync.Mutex
	var signals []string
	var probes []int
	signalGroup = func(_ *supervisor.ProcGroup, _ *os.Process, name string) error {
		mu.Lock()
		defer mu.Unlock()
		signals = append(signals, name)
		return nil
	}
	var inUse bool
	oldInUse := supervisor.SetPidInUseForTest(func(pid int) bool {
		mu.Lock()
		defer mu.Unlock()
		probes = append(probes, pid)
		return inUse
	})
	t.Cleanup(func() { supervisor.SetPidInUseForTest(oldInUse) })
	m := newTestProcManager(t)

	wantKill, wantProbes := "", 0
	if supervisor.KillEndsDrainGroup {
		wantKill, wantProbes = "KILL", 1
	}
	for _, tc := range []struct {
		inUse bool
		want  string
	}{
		{true, ""},
		{false, wantKill},
	} {
		mu.Lock()
		signals, probes, inUse = nil, nil, tc.inUse
		mu.Unlock()
		p := &managedProc{
			id: "DRAIN", running: true, reaped: true,
			cmd: &exec.Cmd{Process: &os.Process{Pid: noSuchPid}}, done: make(chan struct{}),
		}
		m.procs["DRAIN"] = p
		if err := m.kill("DRAIN", "KILL"); err != nil {
			t.Errorf("pid in use %v: kill error %v, want nil", tc.inUse, err)
		}
		mu.Lock()
		got, gotProbes := strings.Join(signals, ","), probes
		mu.Unlock()
		if got != tc.want {
			t.Errorf("pid in use %v: group signals %q, want %q", tc.inUse, got, tc.want)
		}
		if len(gotProbes) != wantProbes || (wantProbes == 1 && gotProbes[0] != noSuchPid) {
			t.Errorf("pid in use %v: probes %v, want %d of pid %d", tc.inUse, gotProbes, wantProbes, noSuchPid)
		}
	}

	// killAndWait keeps its group kill when a live process has the pid.
	mu.Lock()
	signals, probes, inUse = nil, nil, true
	mu.Unlock()
	done := make(chan struct{})
	close(done)
	p := &managedProc{
		id: "DRAIN2", running: true, reaped: true,
		cmd: &exec.Cmd{Process: &os.Process{Pid: noSuchPid}}, done: done,
	}
	m.killAndWaitProc(p, "KILL", time.Second, false)
	mu.Lock()
	got, gotProbes := strings.Join(signals, ","), len(probes)
	mu.Unlock()
	if got != "KILL" || gotProbes != 0 {
		t.Errorf("killAndWait: group signals %q and %d probes, want one KILL and no probe", got, gotProbes)
	}
}
