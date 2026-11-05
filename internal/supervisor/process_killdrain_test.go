package supervisor

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	signalGroup = func(_ *ProcGroup, _ *os.Process, name string) error {
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
		p := &Proc{
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
	oldInUse := SetPidInUseForTest(func(int) bool { return false })
	t.Cleanup(func() { SetPidInUseForTest(oldInUse) })
	var mu sync.Mutex
	var signals []string
	signalGroup = func(_ *ProcGroup, _ *os.Process, name string) error {
		mu.Lock()
		defer mu.Unlock()
		signals = append(signals, name)
		return nil
	}
	m := newTestProcManager(t)

	wantKill := ""
	if KillEndsDrainGroup {
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
		p := &Proc{
			id: "DRAIN", running: tc.running, reaped: true,
			cmd: &exec.Cmd{Process: &os.Process{Pid: 1 << 30}}, done: make(chan struct{}),
		}
		m.procs["DRAIN"] = p
		if err := m.Kill("DRAIN", tc.signal); err != nil {
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
	signalGroup = func(_ *ProcGroup, _ *os.Process, name string) error {
		mu.Lock()
		defer mu.Unlock()
		signals = append(signals, name)
		return nil
	}
	var inUse bool
	oldInUse := SetPidInUseForTest(func(pid int) bool {
		mu.Lock()
		defer mu.Unlock()
		probes = append(probes, pid)
		return inUse
	})
	t.Cleanup(func() { SetPidInUseForTest(oldInUse) })
	m := newTestProcManager(t)

	wantKill, wantProbes := "", 0
	if KillEndsDrainGroup {
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
		p := &Proc{
			id: "DRAIN", running: true, reaped: true,
			cmd: &exec.Cmd{Process: &os.Process{Pid: noSuchPid}}, done: make(chan struct{}),
		}
		m.procs["DRAIN"] = p
		if err := m.Kill("DRAIN", "KILL"); err != nil {
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
	p := &Proc{
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

// TestKillGroupInDrainSendsNothingBeforeTheReap pins the reaped guard of the drain
// kill (claustrum's own). A record that runs and is not reaped yet gets no group
// KILL from killGroupInDrain. Its callers reach it only for a record that is not
// live, so no other test reaches that arm. The control: the same record, once
// reaped, gets the KILL.
func TestKillGroupInDrainSendsNothingBeforeTheReap(t *testing.T) {
	var signals atomic.Int32
	oldSignal := signalGroup
	signalGroup = func(*ProcGroup, *os.Process, string) error { signals.Add(1); return nil }
	t.Cleanup(func() { signalGroup = oldSignal })
	oldInUse := SetPidInUseForTest(func(int) bool { return false })
	t.Cleanup(func() { SetPidInUseForTest(oldInUse) })
	p := &Proc{id: "LIVE", running: true, cmd: &exec.Cmd{Process: &os.Process{Pid: noSuchPid}}}
	p.killGroupInDrain(true)
	p.killGroupInDrain(false)
	if n := signals.Load(); n != 0 {
		t.Errorf("group signals to a record before its reap = %d, want 0", n)
	}
	p.mu.Lock()
	p.reaped = true
	p.mu.Unlock()
	p.killGroupInDrain(false)
	if n := signals.Load(); n != 1 {
		t.Errorf("group signals to the reaped record = %d, want 1", n)
	}
}
