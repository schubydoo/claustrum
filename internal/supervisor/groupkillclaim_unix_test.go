//go:build linux || darwin

package supervisor

import (
	"os"
	"os/exec"
	"slices"
	"sync/atomic"
	"syscall"
	"testing"
)

// TestRefusedKillKeepsTheMarkOfAnEndedGroup: the macOS end marks a group that another end
// killed while a client KILL is under way. The refusal of that KILL leaves the mark, so
// a later KILL sends nothing.
func TestRefusedKillKeepsTheMarkOfAnEndedGroup(t *testing.T) {
	ended := fakeLeftGroup(t, nil)
	p := &Proc{running: true, reaped: true, endsGroup: true, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	old := signalGroup
	var sent atomic.Int32
	signalGroup = func(*ProcGroup, *os.Process, string) error {
		if sent.Add(1) == 1 {
			p.unheldKilled.Store(true)
			p.settleLeftGroupReaped()
			return &SignalFailed{Name: "killed", Err: syscall.EPERM}
		}
		return nil
	}
	t.Cleanup(func() { signalGroup = old })
	p.killGroupInDrain(false)
	if !p.groupKillClaimed.Load() {
		t.Error("the refused kill erased the mark of the ended group")
	}
	p.killGroupInDrain(false)
	if n := sent.Load(); n != 1 {
		t.Errorf("group kills = %d, want only the refused one", n)
	}
	if got := ended(); len(got) != 0 {
		t.Errorf("signals of the end = %v, want none", got)
	}
}

// TestSweepCountsAKillThatLandedInTheHold: a held group that took its SIGKILL before the
// sweep counts as ended and gets no second signal. A landed kill does not count once
// the leader is no longer held, because the sweep tests leaderHeld first.
func TestSweepCountsAKillThatLandedInTheHold(t *testing.T) {
	sweep := fakeLeftGroup(t, nil)
	old := signalGroup
	var sent atomic.Int32
	signalGroup = func(*ProcGroup, *os.Process, string) error { sent.Add(1); return nil }
	t.Cleanup(func() { signalGroup = old })
	newProc := func(held bool) *Proc {
		return &Proc{running: true, reaped: true, leaderHeld: held, endsGroup: true, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	}
	held := newProc(true)
	held.killGroupInDrain(false)
	m := newTestProcManager(t)
	m.procs["held"] = held
	if n := m.KillAllCount(); n != 1 {
		t.Errorf("held: KillAllCount = %d, want 1", n)
	}
	if n := sent.Load(); n != 1 {
		t.Errorf("held: client kills = %d, want 1", n)
	}
	if got := sweep(); len(got) != 0 {
		t.Errorf("held: signals of the sweep = %v, want none", got)
	}
	gone := newProc(false)
	gone.groupKillClaimed.Store(true)
	gone.heldGroupKilled = true
	m2 := newTestProcManager(t)
	m2.procs["gone"] = gone
	if n := m2.KillAllCount(); n != 0 {
		t.Errorf("hold over: KillAllCount = %d, want 0", n)
	}
	if !slices.Equal(sweep(), []syscall.Signal(nil)) {
		t.Errorf("hold over: signals = %v, want none", sweep())
	}
}

// TestRefusedClientKillInTheHoldIsNoCount: a refused client KILL in the hold sets no mark
// for the count. With the claim spent before the sweep, the group then counts 0.
func TestRefusedClientKillInTheHoldIsNoCount(t *testing.T) {
	fakeLeftGroup(t, nil)
	old := signalGroup
	signalGroup = func(*ProcGroup, *os.Process, string) error {
		return &SignalFailed{Name: "killed", Err: syscall.EPERM}
	}
	t.Cleanup(func() { signalGroup = old })
	p := &Proc{running: true, reaped: true, leaderHeld: true, endsGroup: true, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	p.killGroupInDrain(false)
	if p.heldGroupKilled {
		t.Error("a refused client KILL set the mark for the count")
	}
	p.groupKillClaimed.Store(true)
	m := newTestProcManager(t)
	m.procs["refused"] = p
	if n := m.KillAllCount(); n != 0 {
		t.Errorf("KillAllCount = %d, want 0", n)
	}
}

// TestRefusedLateShutdownKillKeepsTheMarkOfTheEnd: the macOS end marks a group that
// another end killed while the SIGKILL of the shutdown wait is under way. The refusal
// of that SIGKILL leaves the mark, so a later one sends nothing.
func TestRefusedLateShutdownKillKeepsTheMarkOfTheEnd(t *testing.T) {
	ended := fakeLeftGroup(t, nil)
	p := &Proc{running: true, endsGroup: true, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	old := signalGroup
	var sent atomic.Int32
	signalGroup = func(*ProcGroup, *os.Process, string) error {
		if sent.Add(1) == 1 {
			p.unheldKilled.Store(true)
			p.settleLeftGroupReaped()
			return &SignalFailed{Name: "killed", Err: syscall.EPERM}
		}
		return nil
	}
	t.Cleanup(func() { signalGroup = old })
	p.killGroupBeforeExitFrame()
	if !p.groupKillClaimed.Load() {
		t.Error("the refused kill erased the mark of the ended group")
	}
	p.killGroupBeforeExitFrame()
	if n := sent.Load(); n != 1 {
		t.Errorf("group kills = %d, want only the refused one", n)
	}
	if got := ended(); len(got) != 0 {
		t.Errorf("signals of the end = %v, want none", got)
	}
}

// TestKeptHeldGroupIsNoCount: with -keep-children a held group with a landed kill does
// not count in the sweep.
func TestKeptHeldGroupIsNoCount(t *testing.T) {
	sent := fakeLeftGroup(t, nil)
	p := &Proc{running: true, reaped: true, leaderHeld: true, keepsGroup: true, heldGroupKilled: true, endsGroup: true, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	p.groupKillClaimed.Store(true)
	m := newTestProcManager(t)
	m.procs["kept"] = p
	if n := m.KillAllCount(); n != 0 {
		t.Errorf("KillAllCount = %d, want 0", n)
	}
	if got := sent(); len(got) != 0 {
		t.Errorf("signals = %v, want none", got)
	}
}
