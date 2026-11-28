//go:build linux || darwin

package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
)

// fakeLeftGroup replaces the kill call. No signal leaves the test.
func fakeLeftGroup(t *testing.T, probe error) func() []syscall.Signal {
	t.Helper()
	real := signalLeftGroup
	var sent []syscall.Signal
	signalLeftGroup = func(_ int, sig syscall.Signal) error {
		sent = append(sent, sig)
		if sig == 0 {
			return probe
		}
		return nil
	}
	t.Cleanup(func() { signalLeftGroup = real })
	return func() []syscall.Signal { return sent }
}

// A pid far above any real one here. The kill call is a fake.
const fakeGroupNumber = 1 << 30

// TestProbeThenKillGroup: 87375cc7 on macOS sends SIGKILL only after a probe that
// returned 0 (measured on macOS). The form runs on Linux too, through the fake.
func TestProbeThenKillGroup(t *testing.T) {
	for _, tc := range []struct {
		name  string
		probe error
		want  []syscall.Signal
	}{
		{"found", nil, []syscall.Signal{0, syscall.SIGKILL}},
		{"no group", syscall.ESRCH, []syscall.Signal{0}},
		{"not allowed", syscall.EPERM, []syscall.Signal{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := fakeLeftGroup(t, tc.probe)
			probeThenKillGroup(fakeGroupNumber)
			if got := sent(); !slices.Equal(got, tc.want) {
				t.Errorf("signals = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGroupNumberRefused: pgid 1 or below, the own pid and the own group get no signal
// (claustrum's own, not measured).
func TestGroupNumberRefused(t *testing.T) {
	for _, pgid := range []int{-1, 0, 1, os.Getpid(), syscall.Getpgrp()} {
		sent := fakeLeftGroup(t, nil)
		if err := signalCheckedGroup(pgid, syscall.SIGKILL); err == nil {
			t.Errorf("pgid %d: no refusal", pgid)
		}
		probeThenKillGroup(pgid)
		if got := sent(); len(got) != 0 {
			t.Errorf("pgid %d: signals = %v, want none", pgid, got)
		}
	}
	if groupNumberRefused(fakeGroupNumber, 100, 200) {
		t.Error("refused a number that is no own one")
	}
}

// TestSettleLeftGroupReapedSkips: the end after the reap sends nothing, not even the probe,
// for a group that another end or the sweep killed, and at a shutdown with
// -keep-children. A leader that exits after the shutdown began still gets the end
// (87375cc7, measured on macOS).
func TestSettleLeftGroupReapedSkips(t *testing.T) {
	stopped := &atomic.Bool{}
	stopped.Store(true)
	tempRoot := &Proc{stopsBegan: &atomic.Bool{}}
	tempRoot.tempRootKilled.Store(true)
	unheld := &Proc{stopsBegan: &atomic.Bool{}}
	unheld.unheldKilled.Store(true)
	swept := &Proc{stopsBegan: stopped}
	swept.sweepKilledGroup.Store(true)
	for name, p := range map[string]*Proc{
		"keep at a shutdown": {stopsBegan: stopped, keepsGroup: true},
		"temp root":          tempRoot,
		"unheld":             unheld,
		"sweep killed":       swept,
	} {
		t.Run(name, func(t *testing.T) {
			sent := fakeLeftGroup(t, nil)
			p.cmd = &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}
			p.settleLeftGroupReaped()
			if got := sent(); len(got) != 0 {
				t.Errorf("signals = %v, want none", got)
			}
		})
	}
	for name, p := range map[string]*Proc{
		"no shutdown":           {stopsBegan: &atomic.Bool{}},
		"shutdown began":        {stopsBegan: stopped},
		"keep with no shutdown": {stopsBegan: &atomic.Bool{}, keepsGroup: true},
	} {
		t.Run(name, func(t *testing.T) {
			sent := fakeLeftGroup(t, nil)
			p.cmd = &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}
			p.settleLeftGroupReaped()
			if got := sent(); !slices.Equal(got, []syscall.Signal{0, syscall.SIGKILL}) {
				t.Errorf("signals = %v, want the probe and the SIGKILL", got)
			}
		})
	}
}

// TestSettleLeftGroupReapedTakesNoLock: the end after the reap runs while another goroutine
// holds p.mu, as emit does across a large marshal. So no lock widens the time between
// the reap and the kill on macOS.
func TestSettleLeftGroupReapedTakesNoLock(t *testing.T) {
	sent := fakeLeftGroup(t, nil)
	p := &Proc{stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	p.mu.Lock()
	done := make(chan struct{})
	go func() { p.settleLeftGroupReaped(); close(done) }()
	select {
	case <-done:
		p.mu.Unlock()
	case <-time.After(2 * time.Second):
		p.mu.Unlock()
		<-done
		t.Fatal("the end after the reap waited for p.mu")
	}
	if got := sent(); !slices.Equal(got, []syscall.Signal{0, syscall.SIGKILL}) {
		t.Errorf("signals = %v, want the probe and the SIGKILL", got)
	}
}

// TestKillHeldGroupAtStop: the shutdown SIGKILL goes once, only while the leader is
// held, and never with -keep-children. A sweep counts the group after that too.
func TestKillHeldGroupAtStop(t *testing.T) {
	newProc := func(held, keep bool) *Proc {
		return &Proc{leaderHeld: held, keepsGroup: keep, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	}
	for _, tc := range []struct {
		name       string
		held, keep bool
		want       []syscall.Signal
		counted    bool
	}{
		{"held", true, false, []syscall.Signal{syscall.SIGKILL}, true},
		{"not held", false, false, nil, false},
		{"kept", true, true, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := fakeLeftGroup(t, nil)
			m := newTestProcManager(t)
			m.procs["held"] = newProc(tc.held, tc.keep)
			want := 0
			if tc.counted {
				want = 1
			}
			for range 2 {
				if n := m.KillAllCount(); n != want {
					t.Errorf("KillAllCount = %d, want %d", n, want)
				}
			}
			if got := sent(); !slices.Equal(got, tc.want) {
				t.Errorf("signals = %v, want %v", got, tc.want)
			}
		})
	}
}

// spawnAskedLauncher starts a stream-json launcher that stays, with a member that
// ignores SIGTERM. The shutdown sweep asks such a leader to end with SIGTERM.
func spawnAskedLauncher(t *testing.T, m *Manager, c *conn.Conn, id, shape string) (*Proc, string) {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "member")
	command := filepath.Join(dir, "cli")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := helperCommand(t, "group-leader")
	launcher := []string{exe}
	p, err := m.spawnWith(c, id, command, []string{pidFile, shape, "ignore", "stay", "stream-json"}, "", env, false, &launcher, 0, TempRootAsk{}, nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() {
		p.signalIfLive("KILL", "")
		select {
		case <-p.done:
		case <-time.After(20 * time.Second):
			t.Error("the launcher child did not end")
		}
	})
	return p, pidFile
}

// exitFrameOut reports whether p buffered its exit frame. A lock that stays taken fails
// the test, and does not count as a frame.
func exitFrameOut(t *testing.T, p *Proc) bool {
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if !p.mu.TryLock() {
			continue
		}
		defer p.mu.Unlock()
		return slices.ContainsFunc(p.buffer, func(f ChildFrame) bool { return f.Stream == "exit" })
	}
	t.Error("p.mu stayed taken, so the exit frame was not read")
	return false
}

// bufferHasExit reports an exit frame in the buffer. The caller holds p.mu.
func bufferHasExit(p *Proc) bool {
	return slices.ContainsFunc(p.buffer, func(f ChildFrame) bool { return f.Stream == "exit" })
}

// recordGroupKills counts each group SIGKILL of the end and of signalGroup, and each
// one after the exit frame of the Proc in cur.
func recordGroupKills(t *testing.T, cur *atomic.Pointer[Proc]) (kills, late *atomic.Int32) {
	t.Helper()
	kills, late = &atomic.Int32{}, &atomic.Int32{}
	realLeft, realGroup := signalLeftGroup, signalGroup
	signalLeftGroup = func(pgid int, sig syscall.Signal) error {
		if sig == syscall.SIGKILL {
			kills.Add(1)
			// The Linux end sends under p.mu, and the macOS end with no lock.
			if p := cur.Load(); p != nil && ((!groupEndAfterReap && bufferHasExit(p)) || (groupEndAfterReap && exitFrameOut(t, p))) {
				late.Add(1)
			}
		}
		return realLeft(pgid, sig)
	}
	// Each caller of signalGroup holds p.mu.
	signalGroup = func(g *ProcGroup, proc *os.Process, name string) error {
		if name == "KILL" {
			kills.Add(1)
			if p := cur.Load(); p != nil && bufferHasExit(p) {
				late.Add(1)
			}
		}
		return realGroup(g, proc, name)
	}
	t.Cleanup(func() { signalLeftGroup, signalGroup = realLeft, realGroup })
	return kills, late
}

// TestAskedLauncherKillComesBeforeTheExitFrame: a launcher that the sweep asked to end
// gets one group SIGKILL, from the end or from endAsked, before its exit frame, on Linux
// and on macOS. The shape "two" has a holder of the pipes in its own session.
// The log counts the child as exited (87375cc7, measured on Linux and macOS).
func TestAskedLauncherKillComesBeforeTheExitFrame(t *testing.T) {
	for _, shape := range []string{"closed", "two"} {
		t.Run(shape, func(t *testing.T) {
			oldDrain := SetExitDrainGraceForTest(2500 * time.Millisecond)
			oldGrace := shutdownEndGrace
			shutdownEndGrace = 800 * time.Millisecond
			t.Cleanup(func() {
				SetExitDrainGraceForTest(oldDrain)
				shutdownEndGrace = oldGrace
			})
			var cur atomic.Pointer[Proc]
			kills, late := recordGroupKills(t, &cur)
			buf := captureLogBuf(t)
			m := newTestProcManager(t)
			t.Cleanup(m.KillAll)
			c, lines := rawLineConn(t)
			p, pidFile := spawnAskedLauncher(t, m, c, "lgf"+shape, shape)
			cur.Store(p)
			member := waitPIDFile(t, pidFile)
			t.Cleanup(func() { _ = syscall.Kill(member, syscall.SIGKILL) })
			if shape == "two" {
				holder := waitPIDFile(t, pidFile+".holder")
				t.Cleanup(func() { _ = syscall.Kill(holder, syscall.SIGKILL) })
			}
			nextLine(t, lines, `"stream":"stdout"`, 15*time.Second)
			m.StopStarts()
			m.KillAllCount()
			select {
			case <-p.done:
			case <-time.After(20 * time.Second):
				t.Fatal("no exit frame")
			}
			if n := kills.Load(); n != 1 {
				t.Errorf("group SIGKILLs = %d, want 1", n)
			}
			if n := late.Load(); n != 0 {
				t.Errorf("%d group SIGKILL after the exit frame", n)
			}
			if want := "asked 1 Claude Code process(es) to end: 1 exited, 0 had to be killed"; !strings.Contains(buf.String(), want) {
				t.Errorf("log has no %q", want)
			}
		})
	}
}

// TestLauncherLeaderGoneCountsOnlyAnExitedLauncher: endAsked counts a launcher whose
// leader left as exited. A plain spawn in its drain, and a launcher that runs, do not.
func TestLauncherLeaderGoneCountsOnlyAnExitedLauncher(t *testing.T) {
	for _, tc := range []struct {
		endsGroup, reaped, want bool
	}{{true, true, true}, {false, true, false}, {true, false, false}} {
		p := &Proc{endsGroup: tc.endsGroup, reaped: tc.reaped}
		if got := p.launcherLeaderGone(); got != tc.want {
			t.Errorf("endsGroup %v, reaped %v: got %v", tc.endsGroup, tc.reaped, got)
		}
	}
}

// TestOneGroupKillClaim: once the end took the claim, killGroupBeforeExitFrame sends
// nothing. A spawn with no end still gets its SIGKILL, and the end after the reap
// sends no probe after a claim of endAsked.
func TestOneGroupKillClaim(t *testing.T) {
	old := signalGroup
	var sent atomic.Int32
	signalGroup = func(*ProcGroup, *os.Process, string) error { sent.Add(1); return nil }
	t.Cleanup(func() { signalGroup = old })
	newProc := func(ends bool) *Proc {
		return &Proc{running: true, endsGroup: ends, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	}
	claimed := newProc(true)
	claimed.groupKillClaimed.Store(true)
	claimed.killGroupBeforeExitFrame()
	if n := sent.Swap(0); n != 0 {
		t.Errorf("after the claim: %d signals, want none", n)
	}
	free := newProc(true)
	free.killGroupBeforeExitFrame()
	free.killGroupBeforeExitFrame()
	if n := sent.Swap(0); n != 1 {
		t.Errorf("launcher: %d signals, want 1", n)
	}
	plain := newProc(false)
	plain.killGroupBeforeExitFrame()
	plain.killGroupBeforeExitFrame()
	if n := sent.Swap(0); n != 2 {
		t.Errorf("no end: %d signals, want 2 as before", n)
	}
	probes := fakeLeftGroup(t, nil)
	// The claim of endAsked comes before its stamp, so only the claim shows here.
	taken := newProc(true)
	taken.groupKillClaimed.Store(true)
	taken.settleLeftGroupReaped()
	if got := probes(); len(got) != 0 {
		t.Errorf("after a claim of endAsked: signals = %v, want no probe", got)
	}
	// A group that another end killed takes the claim, so endAsked adds no SIGKILL.
	unheld := newProc(true)
	unheld.unheldKilled.Store(true)
	unheld.settleLeftGroupReaped()
	unheld.killGroupBeforeExitFrame()
	if n := sent.Load(); n != 0 {
		t.Errorf("after the unattended end: %d signals, want none", n)
	}
}

// TestSweepCountsAHoldTakenBetweenItsTests: a leader that the exit goroutine holds after
// the first test of the sweep still gets the SIGKILL and counts as ended.
func TestSweepCountsAHoldTakenBetweenItsTests(t *testing.T) {
	sent := fakeLeftGroup(t, nil)
	old := beforeSweepSignal
	beforeSweepSignal = func(p *Proc) {
		p.mu.Lock()
		p.reaped, p.leaderHeld = true, true
		p.mu.Unlock()
	}
	t.Cleanup(func() { beforeSweepSignal = old })
	m := newTestProcManager(t)
	m.procs["late"] = &Proc{running: true, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	if n := m.KillAllCount(); n != 1 {
		t.Errorf("KillAllCount = %d, want 1", n)
	}
	if got := sent(); !slices.Equal(got, []syscall.Signal{syscall.SIGKILL}) {
		t.Errorf("signals = %v, want one SIGKILL", got)
	}
}

// TestSweepStampsOnlyItsGroupKill: a SIGTERM of the sweep to the leader alone does not
// count as a killed group, and a SIGKILL does.
func TestSweepStampsOnlyItsGroupKill(t *testing.T) {
	old := signalGroup
	signalGroup = func(*ProcGroup, *os.Process, string) error { return nil }
	t.Cleanup(func() { signalGroup = old })
	p := &Proc{running: true, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	p.signalIfLive("TERM", "shutdown")
	if p.sweepKilledGroup.Load() {
		t.Error("a SIGTERM counts as a killed group")
	}
	p.signalIfLive("KILL", "shutdown")
	if !p.sweepKilledGroup.Load() {
		t.Error("the SIGKILL of the sweep is not stamped")
	}
}

// TestDrainKillsHonorTheClaim: for a launcher with the end on, killGroupInDrain and
// killGroupAfterExit send the one SIGKILL after the exit, and nothing once it is claimed.
// A refused kill gives the claim back. With no end they send each time, as before.
func TestDrainKillsHonorTheClaim(t *testing.T) {
	old := signalGroup
	var sent atomic.Int32
	var refuse atomic.Bool
	signalGroup = func(*ProcGroup, *os.Process, string) error {
		sent.Add(1)
		if refuse.Load() {
			return &SignalFailed{Name: "killed", Err: syscall.EPERM}
		}
		return nil
	}
	t.Cleanup(func() { signalGroup = old })
	newProc := func(ends bool) *Proc {
		return &Proc{running: true, reaped: true, endsGroup: ends, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	}
	paths := map[string]func(*Proc){
		"drain":      func(p *Proc) { _ = p.killGroupInDrain(false) },
		"after exit": func(p *Proc) { _ = p.killGroupAfterExit() },
	}
	for name, kill := range paths {
		t.Run(name, func(t *testing.T) {
			sent.Store(0)
			taken := newProc(true)
			taken.groupKillClaimed.Store(true)
			kill(taken)
			if n := sent.Swap(0); n != 0 {
				t.Errorf("claimed: %d signals, want none", n)
			}
			free := newProc(true)
			kill(free)
			kill(free)
			if n := sent.Swap(0); n != 1 || !free.groupKillClaimed.Load() {
				t.Errorf("free: %d signals, claim %v, want 1 and taken", n, free.groupKillClaimed.Load())
			}
			refuse.Store(true)
			refused := newProc(true)
			kill(refused)
			refuse.Store(false)
			if refused.groupKillClaimed.Load() {
				t.Error("a refused kill keeps the claim")
			}
			sent.Store(0)
			plain := newProc(false)
			plain.groupKillClaimed.Store(true)
			kill(plain)
			kill(plain)
			if n := sent.Swap(0); n != 2 {
				t.Errorf("no end: %d signals, want 2 as on main", n)
			}
		})
	}
	// A kill that the pid test skips takes no claim.
	oldPid := SetPidInUseForTest(func(int) bool { return true })
	t.Cleanup(func() { SetPidInUseForTest(oldPid) })
	sent.Store(0)
	skipped := newProc(true)
	_ = skipped.killGroupInDrain(true)
	if sent.Load() != 0 || skipped.groupKillClaimed.Load() {
		t.Errorf("skipped kill: %d signals, claim %v", sent.Load(), skipped.groupKillClaimed.Load())
	}
}

// TestRefusedShutdownKillKeepsTheOneKill: a group SIGKILL of the shutdown wait that was
// refused leaves the claim free and marks no killed group, so the end after the reap
// still sends its probe and its SIGKILL.
func TestRefusedShutdownKillKeepsTheOneKill(t *testing.T) {
	old := signalGroup
	signalGroup = func(*ProcGroup, *os.Process, string) error {
		return &SignalFailed{Name: "killed", Err: syscall.EPERM}
	}
	t.Cleanup(func() { signalGroup = old })
	sent := fakeLeftGroup(t, nil)
	p := &Proc{running: true, endsGroup: true, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	p.killGroupBeforeExitFrame()
	if p.groupKillClaimed.Load() {
		t.Error("a refused kill keeps the claim")
	}
	if p.sweepKilledGroup.Load() {
		t.Error("a refused kill counts as a killed group")
	}
	p.settleLeftGroupReaped()
	if got := sent(); !slices.Equal(got, []syscall.Signal{0, syscall.SIGKILL}) {
		t.Errorf("signals of the end = %v, want the probe and the SIGKILL", got)
	}
}

// TestNoSignalPassesTheGuardAfterTheMacOSReap: between the end after the reap and the
// reaped mark, a client KILL and the sweep send no second group SIGKILL and claim no
// exit. This holds on each way through the end.
func TestNoSignalPassesTheGuardAfterTheMacOSReap(t *testing.T) {
	old := signalGroup
	var sent atomic.Int32
	signalGroup = func(*ProcGroup, *os.Process, string) error { sent.Add(1); return nil }
	t.Cleanup(func() { signalGroup = old })
	for name, prepare := range map[string]func(*Proc){
		"the end sends":       func(*Proc) {},
		"another end killed":  func(p *Proc) { p.unheldKilled.Store(true) },
		"the claim was taken": func(p *Proc) { p.groupKillClaimed.Store(true) },
	} {
		t.Run(name, func(t *testing.T) {
			fakeLeftGroup(t, nil)
			sent.Store(0)
			p := &Proc{running: true, endsGroup: true, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
			prepare(p)
			p.settleLeftGroupReaped()
			if live, err := p.signalIfLiveErr("KILL", "client"); live || err != nil {
				t.Errorf("client KILL: live %v, err %v, want not live", live, err)
			}
			m := newTestProcManager(t)
			m.procs["reaped"] = p
			if n := m.KillAllCount(); n != 0 {
				t.Errorf("KillAllCount = %d, want 0", n)
			}
			if n := sent.Load(); n != 0 {
				t.Errorf("%d group signals after the end, want none", n)
			}
			if p.KilledByForTest() != "" {
				t.Errorf("killedBy = %q, want none", p.KilledByForTest())
			}
		})
	}
}

// TestEscalationAfterTheMacOSReapIsNotARunningLeader: a pid that shows a running process
// after the reap can be a new process. The escalation then goes through the claim. The
// child is the test's own, and the group signal is a fake.
func TestEscalationAfterTheMacOSReapIsNotARunningLeader(t *testing.T) {
	old := signalGroup
	var sent atomic.Int32
	signalGroup = func(*ProcGroup, *os.Process, string) error { sent.Add(1); return nil }
	t.Cleanup(func() { signalGroup = old })
	exe, env := helperCommand(t, "group-leader")
	cmd := exec.Command(exe, "cli", "unused", "none", "exit", "stay")
	cmd.Env = BuildEnv(env)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	p := &Proc{running: true, endsGroup: true, stopsBegan: &atomic.Bool{}, cmd: cmd}
	p.leaderReapSeen.Store(true)
	if err := p.killGroupAfterExit(); err != nil {
		t.Fatal(err)
	}
	if err := p.killGroupAfterExit(); err != nil {
		t.Fatal(err)
	}
	if n := sent.Load(); n != 1 || !p.groupKillClaimed.Load() {
		t.Errorf("%d signals, claim %v, want one signal through the claim", n, p.groupKillClaimed.Load())
	}
}
