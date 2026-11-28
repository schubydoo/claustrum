//go:build linux || darwin

package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// drainMemberPid reads the pid of an own member and ends it at the end of the test,
// only while it still lives.
func drainMemberPid(t *testing.T, pidFile string) int {
	t.Helper()
	pid := waitPIDFile(t, pidFile)
	t.Cleanup(func() {
		if memberLives(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return pid
}

func awaitDrainDone(t *testing.T, p *Proc) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(20 * time.Second):
		t.Fatal("no exit frame")
	}
}

func awaitDrainMemberGone(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); memberLives(pid); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("member %d lives on", pid)
		}
	}
}

// spawnPlainWithMember starts a plain child through the helper of the group tests.
// shape is "hold" (the member keeps stdout and stderr) or "closed" (its own stdio).
// The member ignores SIGTERM. leader is "go" (exit at once) or "stay".
func spawnPlainWithMember(t *testing.T, m *Manager, shape, leader string, extra ...string) (*Proc, int) {
	t.Helper()
	return spawnAskWithMember(t, m, TempRootAsk{}, shape, leader, extra...)
}

// spawnAskWithMember is spawnPlainWithMember with the members of the request.
func spawnAskWithMember(t *testing.T, m *Manager, ask TempRootAsk, shape, leader string, extra ...string) (*Proc, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "member")
	exe, env := helperCommand(t, "group-leader")
	c, _ := rawLineConn(t)
	args := append([]string{"cli", pidFile, shape, "ignore", leader}, extra...)
	p, err := m.SpawnWithTempRoot(c, "plain-"+shape, exe, args, "", env, false, nil, 0, ask, nil, 0)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// The exit goroutine reads the seams, so it ends before a test restores them.
	t.Cleanup(func() {
		p.signalIfLive("KILL", "")
		select {
		case <-p.done:
		case <-time.After(20 * time.Second):
			t.Error("the child did not end")
		}
	})
	return p, drainMemberPid(t, pidFile)
}

// awaitLeaderReaped polls until the exit goroutine marked the leader reaped.
func awaitLeaderReaped(t *testing.T, p *Proc) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		p.mu.Lock()
		reaped := p.reaped
		p.mu.Unlock()
		if reaped {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the leader was not reaped")
		}
	}
}

// memberLives asks ps, on Linux and macOS. A zombie counts as gone.
func memberLives(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	s := strings.TrimSpace(string(out))
	return err == nil && s != "" && s[0] != 'Z'
}

// countDrainKills replaces signalGroup with a counter that delivers nothing, so the
// member keeps the drain open. It is called before the spawn, so it is restored last.
func countDrainKills(t *testing.T) func() int {
	t.Helper()
	real := signalGroup
	var mu sync.Mutex
	n := 0
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		mu.Lock()
		defer mu.Unlock()
		if signame == "KILL" {
			n++
		}
		return nil
	}
	t.Cleanup(func() { signalGroup = real })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// TestShutdownKillsThePlainGroupInItsDrain: 87375cc7 and 4c111e86 on Linux. The leader
// is reaped and a member holds the pipes. The shutdown sends one group SIGKILL.
func TestShutdownKillsThePlainGroupInItsDrain(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	p, member := spawnPlainWithMember(t, m, "hold", "go")
	awaitLeaderReaped(t, p)
	sent := recordShutdownSignals(t, true, p)
	if n := m.KillAllCount(); n != 1 {
		t.Errorf("killAllCount = %d, want 1", n)
	}
	awaitDrainMemberGone(t, member)
	awaitDrainDone(t, p)
	if got := sent(p); !slices.Equal(got, []string{"KILL"}) {
		t.Errorf("signals = %q, want one KILL", got)
	}
}

// TestShutdownSendsNoKillAfterTheExitFrame: after the exit frame the member gets no
// signal and lives on (87375cc7 and 4c111e86, measured on Linux).
func TestShutdownSendsNoKillAfterTheExitFrame(t *testing.T) {
	old := exitDrainGrace
	exitDrainGrace = 150 * time.Millisecond
	t.Cleanup(func() { exitDrainGrace = old })
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	p, member := spawnPlainWithMember(t, m, "hold", "go")
	awaitDrainDone(t, p)
	sent := recordShutdownSignals(t, true, p)
	if n := m.KillAllCount(); n != 0 {
		t.Errorf("killAllCount = %d, want 0", n)
	}
	if got := sent(p); len(got) != 0 {
		t.Errorf("signals = %q, want none", got)
	}
	if !memberLives(member) {
		t.Error("the member ended, want it alive after the exit frame")
	}
}

// TestShutdownKeepChildrenSendsNoDrainKill: with -keep-children the group gets nothing.
func TestShutdownKeepChildrenSendsNoDrainKill(t *testing.T) {
	old := exitDrainGrace
	exitDrainGrace = 2 * time.Second
	t.Cleanup(func() { exitDrainGrace = old })
	count := countDrainKills(t)
	m := newTestProcManager(t)
	m.KeepChildren = true
	p, member := spawnPlainWithMember(t, m, "hold", "go")
	awaitLeaderReaped(t, p)
	if p.killGroupInOpenDrain() {
		t.Error("the drain kill reported a send with -keep-children")
	}
	if n := m.KillAllCount(); n != 0 {
		t.Errorf("killAllCount = %d, want 0", n)
	}
	if got := count(); got != 0 {
		t.Errorf("KILL sends = %d, want 0", got)
	}
	if !memberLives(member) {
		t.Error("the member ended, want it kept")
	}
}

// startBystander starts a helper sleep in its own process group, outside every group
// of the test spawns. It ends at the end of the test.
func startBystander(t *testing.T) int {
	t.Helper()
	exe, env := helperCommand(t, "sleep")
	cmd := exec.Command(exe, "60")
	cmd.Env = BuildEnv(env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("bystander: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// TestShutdownKillsTheGroupOfAChildThatEndedOnTheTerm: a stream-json child ends on the
// SIGTERM and its member has its own stdio. Its group gets one SIGKILL after the exit,
// and the member ends (87375cc7 and 13047a51, measured on Linux and macOS). A process
// in another group lives on.
func TestShutdownKillsTheGroupOfAChildThatEndedOnTheTerm(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	bystander := startBystander(t)
	p, member := spawnPlainWithMember(t, m, "closed", "stay", "stream-json")
	sent := recordShutdownSignals(t, true, p)
	if n := m.KillAllCount(); n != 1 {
		t.Errorf("killAllCount = %d, want 1", n)
	}
	awaitDrainDone(t, p)
	awaitDrainMemberGone(t, member)
	if got := sent(p); !slices.Equal(got, []string{"TERM", "KILL"}) {
		t.Errorf("signals = %q, want TERM, KILL", got)
	}
	if !memberLives(bystander) {
		t.Error("the bystander in another group ended, want it alive")
	}
}

// TestShutdownAskedChildGetsTheDrainKillAtOnce: a stream-json child ends on the SIGTERM
// and its member holds the pipes. The group SIGKILL follows the reap, with no wait for
// the end of the grace (87375cc7, measured on Linux).
func TestShutdownAskedChildGetsTheDrainKillAtOnce(t *testing.T) {
	logs := captureLogBuf(t)
	old := shutdownEndGrace
	shutdownEndGrace = 30 * time.Second
	t.Cleanup(func() { shutdownEndGrace = old })
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	p, member := spawnPlainWithMember(t, m, "hold", "stay", "stream-json")
	sent := recordShutdownSignals(t, true, p)
	start := time.Now()
	if n := m.KillAllCount(); n != 1 {
		t.Errorf("killAllCount = %d, want 1", n)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the sweep took %v, want the kill at the reap", d)
	}
	awaitDrainMemberGone(t, member)
	if got := sent(p); !slices.Equal(got, []string{"TERM", "KILL"}) {
		t.Errorf("signals = %q, want TERM, KILL", got)
	}
	if want := "asked 1 Claude Code process(es) to end: 1 exited, 0 had to be killed"; !strings.Contains(logs.String(), want) {
		t.Errorf("log has no %q", want)
	}
}

// refuseGroupKills makes each KILL of signalGroup fail with EPERM, with nothing sent.
// Every other signal goes on to its target.
func refuseGroupKills(t *testing.T) {
	t.Helper()
	inner := signalGroup
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		if signame == "KILL" {
			return &SignalFailed{Name: "killed", Err: syscall.EPERM}
		}
		return inner(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = inner })
}

// TestShutdownWaitEndsAtTheReapAfterARefusedDrainKill: a stream-json child ends on the
// SIGTERM and its member holds the pipes. The drain kill is refused. The wait ends at the
// reap and counts the child as exited (87375cc7, measured on Linux).
func TestShutdownWaitEndsAtTheReapAfterARefusedDrainKill(t *testing.T) {
	logs := captureLogBuf(t)
	oldEnd, oldDrain := shutdownEndGrace, exitDrainGrace
	shutdownEndGrace, exitDrainGrace = 8*time.Second, 30*time.Second
	t.Cleanup(func() { shutdownEndGrace, exitDrainGrace = oldEnd, oldDrain })
	m := newTestProcManager(t)
	p, _ := spawnPlainWithMember(t, m, "hold", "stay", "stream-json")
	refuseGroupKills(t)
	start := time.Now()
	m.KillAllCount()
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("the sweep took %v, want its end at the reap", d)
	}
	if want := "asked 1 Claude Code process(es) to end: 1 exited, 0 had to be killed"; !strings.Contains(logs.String(), want) {
		t.Errorf("log has no %q:\n%s", want, logs.String())
	}
	if !p.groupKillClaimed.spent() {
		t.Error("the drain kill took no claim")
	}
}

// TestShutdownCountsNoRefusedSweepKill: a live leader whose sweep SIGKILL is refused is no ended
// group in the count of the cleanup line. 87375cc7 does so after a refused client KILL (measured on Linux), and with none first, as here, it is claustrum's own.
func TestShutdownCountsNoRefusedSweepKill(t *testing.T) {
	m := newTestProcManager(t)
	spawnPlainWithMember(t, m, "closed", "stay")
	refuseGroupKills(t)
	if n := m.KillAllCount(); n != 0 {
		t.Errorf("killAllCount = %d, want 0", n)
	}
}

// TestShutdownCountsARefusedSweepTerm: a stream-json child that refuses its sweep SIGTERM
// still counts in the cleanup line, as before (claustrum's own, not measured). The seam
// refuses the SIGTERM, and the KILL at the end of the short wait reaches the test's own child.
func TestShutdownCountsARefusedSweepTerm(t *testing.T) {
	old := shutdownEndGrace
	shutdownEndGrace = 100 * time.Millisecond
	t.Cleanup(func() { shutdownEndGrace = old })
	m := newTestProcManager(t)
	spawnPlainWithMember(t, m, "closed", "stay", "stream-json")
	inner := signalGroup
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		if signame == "TERM" {
			return &SignalFailed{Name: "terminated", Err: syscall.EPERM}
		}
		return inner(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = inner })
	if n := m.KillAllCount(); n != 1 {
		t.Errorf("killAllCount = %d, want 1", n)
	}
}

// TestShutdownWaitRunsToItsEdgeWithNoDrainKill: with -keep-children no drain kill takes the
// claim of a stream-json child whose member holds the pipes. The wait then runs to its edge
// and counts the child as killed, as before. The seam refuses each KILL.
func TestShutdownWaitRunsToItsEdgeWithNoDrainKill(t *testing.T) {
	logs := captureLogBuf(t)
	oldEnd, oldDrain := shutdownEndGrace, exitDrainGrace
	shutdownEndGrace, exitDrainGrace = 1500*time.Millisecond, 30*time.Second
	t.Cleanup(func() { shutdownEndGrace, exitDrainGrace = oldEnd, oldDrain })
	m := newTestProcManager(t)
	m.KeepChildren = true
	p, _ := spawnPlainWithMember(t, m, "hold", "stay", "stream-json")
	refuseGroupKills(t)
	start := time.Now()
	m.KillAllCount()
	if d := time.Since(start); d < 1400*time.Millisecond {
		t.Errorf("the sweep took %v, want the whole wait", d)
	}
	if want := "asked 1 Claude Code process(es) to end: 0 exited, 1 had to be killed"; !strings.Contains(logs.String(), want) {
		t.Errorf("log has no %q:\n%s", want, logs.String())
	}
	if p.groupKillClaimed.Load() {
		t.Error("a drain kill took the claim with -keep-children")
	}
}

// TestDrainKillGoesOutOnce: every path that can follow sends no second SIGKILL.
func TestDrainKillGoesOutOnce(t *testing.T) {
	count := countDrainKills(t)
	m := newTestProcManager(t)
	p, _ := spawnPlainWithMember(t, m, "hold", "go")
	awaitLeaderReaped(t, p)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { p.killGroupInOpenDrain() })
	}
	wg.Wait()
	m.KillAllCount()
	_ = p.killGroupInDrain(false)
	p.killGroupBeforeExitFrame()
	_ = p.killGroupAfterExit()
	if got := count(); got != 1 {
		t.Errorf("KILL sends = %d, want 1", got)
	}
}

// TestShutdownAddsAKillAfterAClientKillInTheDrain: a client KILL inside the drain and
// then a shutdown make two group kills (87375cc7, measured on Linux). The seam delivers
// nothing.
func TestShutdownAddsAKillAfterAClientKillInTheDrain(t *testing.T) {
	count := countDrainKills(t)
	m := newTestProcManager(t)
	p, _ := spawnPlainWithMember(t, m, "hold", "go")
	awaitLeaderReaped(t, p)
	_ = p.killGroupInDrain(false)
	if n := m.KillAllCount(); n != 1 {
		t.Errorf("killAllCount = %d, want 1", n)
	}
	if got := count(); got != 2 {
		t.Errorf("KILL sends = %d, want 2", got)
	}
}

// TestDrainKillSkipsAPtySpawn: a spawn with a pty object gets no group kill in its drain
// (87375cc7, measured on Linux and macOS). The seam delivers nothing.
func TestDrainKillSkipsAPtySpawn(t *testing.T) {
	count := countDrainKills(t)
	m := newTestProcManager(t)
	p, _ := spawnAskWithMember(t, m, TempRootAsk{Pty: true}, "hold", "go")
	awaitLeaderReaped(t, p)
	if n := m.KillAllCount(); n != 0 {
		t.Errorf("killAllCount = %d, want 0", n)
	}
	if got := count(); got != 0 {
		t.Errorf("KILL sends = %d, want 0", got)
	}
}

// TestDrainKillRefusals: no send for a leader that left the group, after the exit
// frame, for another kind of spawn, or after the sweep's kill.
func TestDrainKillRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(t *testing.T, p *Proc)
	}{
		{"left the group", func(_ *testing.T, p *Proc) {
			p.mu.Lock()
			p.leaderKeptGroup = false
			p.mu.Unlock()
		}},
		{"exit frame out", func(_ *testing.T, p *Proc) {
			p.mu.Lock()
			p.running = false
			p.mu.Unlock()
		}},
		{"not a plain spawn", func(_ *testing.T, p *Proc) {
			p.mu.Lock()
			p.plainGroupEnd = false
			p.mu.Unlock()
		}},
		{"the sweep killed the group", func(_ *testing.T, p *Proc) { p.sweepKilledGroup.Store(true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := countDrainKills(t)
			m := newTestProcManager(t)
			p, _ := spawnPlainWithMember(t, m, "hold", "go")
			awaitLeaderReaped(t, p)
			tc.set(t, p)
			if p.killGroupInOpenDrain() {
				t.Error("the drain kill reported a send")
			}
			if got := count(); got != 0 {
				t.Errorf("KILL sends = %d, want 0", got)
			}
		})
	}
}

// TestDrainKillRefusesTheOwnGroupID: a number that names this daemon gets no signal,
// also with the pipe held. The Proc is built by hand and the seam delivers nothing.
func TestDrainKillRefusesTheOwnGroupID(t *testing.T) {
	count := countDrainKills(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	p := &Proc{
		id: "own", plainGroupEnd: true, leaderKeptGroup: true, running: true, reaped: true,
		cmd: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}, group: &ProcGroup{},
		outPipes: [2]*os.File{r, nil},
	}
	if p.killGroupInOpenDrain() {
		t.Error("the drain kill reported a send to the own group number")
	}
	if got := count(); got != 0 {
		t.Errorf("KILL sends = %d, want 0", got)
	}
}

// TestDrainKillSendsToAReusedGroupID: a process at the freed number gets no test, and
// the signal goes out, as on 87375cc7 (measured on Linux). The seam delivers nothing,
// and the probe seam fails the test if this path asks it.
func TestDrainKillSendsToAReusedGroupID(t *testing.T) {
	count := countDrainKills(t)
	old := pidInUse
	var asked atomic.Int32
	pidInUse = func(int) bool { asked.Add(1); return true }
	t.Cleanup(func() { pidInUse = old })
	m := newTestProcManager(t)
	p, _ := spawnPlainWithMember(t, m, "hold", "go")
	awaitLeaderReaped(t, p)
	if !p.killGroupInOpenDrain() {
		t.Error("the drain kill sent nothing to a number with a live process")
	}
	if got := count(); got != 1 {
		t.Errorf("KILL sends = %d, want 1", got)
	}
	if got := asked.Load(); got != 0 {
		t.Errorf("the path probed the pid %d times, want none", got)
	}
}

// spawnPlainWithLoneHolder starts the shape "two": a member in the group on its own
// stdio, and a holder of the pipes in its own session. It returns both pids.
func spawnPlainWithLoneHolder(t *testing.T, m *Manager, leader string) (*Proc, int, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "member")
	exe, env := helperCommand(t, "group-leader")
	c, _ := rawLineConn(t)
	p, err := m.Spawn(c, "plain-two", exe, []string{"cli", pidFile, "two", "ignore", leader}, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-p.done:
		case <-time.After(20 * time.Second):
			t.Error("the child did not end")
		}
	})
	member := drainMemberPid(t, pidFile)
	// Registered last, so the holder ends first and the drain closes.
	holder := drainMemberPid(t, pidFile+".holder")
	return p, member, holder
}

// TestDrainKillEndsAMemberWhenALoneHolderKeepsThePipes: the holder is in its own session
// and the member is on its own stdio. The group SIGKILL ends the member, and the holder
// lives on (87375cc7, measured on Linux).
func TestDrainKillEndsAMemberWhenALoneHolderKeepsThePipes(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	p, member, holder := spawnPlainWithLoneHolder(t, m, "go")
	awaitLeaderReaped(t, p)
	sent := recordShutdownSignals(t, true, p)
	if n := m.KillAllCount(); n != 1 {
		t.Errorf("killAllCount = %d, want 1", n)
	}
	awaitDrainMemberGone(t, member)
	if got := sent(p); !slices.Equal(got, []string{"KILL"}) {
		t.Errorf("signals = %q, want one KILL", got)
	}
	if !memberLives(holder) {
		t.Error("the holder in its own session ended, want it alive")
	}
}

// TestDrainKillComesAfterAKillOfTheRunningLeader: a client KILL reaches the running leader
// and its group, and a holder in its own session keeps the drain open. The shutdown then
// sends a second group kill (87375cc7, measured on Linux). Only the first kill is
// delivered, because the group is empty at the second and its number is free.
func TestDrainKillComesAfterAKillOfTheRunningLeader(t *testing.T) {
	real := signalGroup
	var mu sync.Mutex
	kills := 0
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		mu.Lock()
		defer mu.Unlock()
		if signame != "KILL" {
			return real(g, proc, signame)
		}
		kills++
		if kills == 1 {
			return real(g, proc, signame)
		}
		return nil
	}
	t.Cleanup(func() { signalGroup = real })
	m := newTestProcManager(t)
	p, member, holder := spawnPlainWithLoneHolder(t, m, "stay")
	if err := m.Kill(p.id, "KILL"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	awaitDrainMemberGone(t, member)
	awaitLeaderReaped(t, p)
	if n := m.KillAllCount(); n != 1 {
		t.Errorf("killAllCount = %d, want 1", n)
	}
	mu.Lock()
	got := kills
	mu.Unlock()
	if got != 2 {
		t.Errorf("KILL sends = %d, want 2", got)
	}
	if !memberLives(holder) {
		t.Error("the holder in its own session ended, want it alive")
	}
}

// TestDrainKillInTheGapAfterTheGrace: the grace closed the read ends and the exit frame
// is not out. A shutdown there still sends the group SIGKILL (claustrum's rule at the 5 s edge,
// where 87375cc7 sends the kill in some runs and not in others, measured on Linux). The seam holds the exit goroutine in the gap and delivers no signal.
func TestDrainKillInTheGapAfterTheGrace(t *testing.T) {
	old := exitDrainGrace
	exitDrainGrace = 150 * time.Millisecond
	t.Cleanup(func() { exitDrainGrace = old })
	count := countDrainKills(t)
	inGap, leave := make(chan struct{}), make(chan struct{})
	oldGap := afterGraceClose
	afterGraceClose = func(p *Proc) {
		if p.id == "plain-hold" {
			close(inGap)
			<-leave
		}
	}
	t.Cleanup(func() { afterGraceClose = oldGap })
	m := newTestProcManager(t)
	p, _ := spawnPlainWithMember(t, m, "hold", "go")
	var once sync.Once
	release := func() { once.Do(func() { close(leave) }) }
	defer release()
	select {
	case <-inGap:
	case <-time.After(10 * time.Second):
		t.Fatal("the grace did not end")
	}
	if pipesStillHeld(p.outPipes) {
		t.Fatal("the read ends are still open in the gap")
	}
	if !p.IsRunning() {
		t.Fatal("running turned false before the end of the gap")
	}
	if n := m.KillAllCount(); n != 1 {
		t.Errorf("killAllCount = %d, want 1", n)
	}
	if got := count(); got != 1 {
		t.Errorf("KILL sends = %d, want 1", got)
	}
}

// TestGraceMarkNeedsAHeldPipe: a pipe with a writer at the grace counts. A pipe at its
// end does not, so a member on its own stdio gets no send. The Procs are built by
// hand, and the seam delivers nothing.
func TestGraceMarkNeedsAHeldPipe(t *testing.T) {
	count := countDrainKills(t)
	for _, tc := range []struct {
		name  string
		held  bool
		sends int
	}{{"writer gone", false, 0}, {"writer held", true, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			before := count()
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if tc.held {
				defer w.Close()
			} else if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			p := &Proc{
				id: "grace", plainGroupEnd: true, leaderKeptGroup: true, running: true, reaped: true,
				cmd: &exec.Cmd{Process: &os.Process{Pid: 999999}}, group: &ProcGroup{},
				outPipes: [2]*os.File{r, nil},
			}
			p.notePipesHeldAtGrace()
			if p.pipesHeldAtGrace != tc.held {
				t.Errorf("pipesHeldAtGrace = %v, want %v", p.pipesHeldAtGrace, tc.held)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			p.killGroupInOpenDrain()
			if got := count() - before; got != tc.sends {
				t.Errorf("KILL sends = %d, want %d", got, tc.sends)
			}
		})
	}
}

// TestPipeTestPollsAgainAfterEINTR: a poll that EINTR stops does not read as "no writer".
// The retry has a bound, and any other error stops it at once. The pipe is the test's own.
func TestPipeTestPollsAgainAfterEINTR(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	real := pollPipe
	t.Cleanup(func() { pollPipe = real })
	for _, tc := range []struct {
		name      string
		failures  int
		failure   error
		wantHeld  bool
		wantCalls int
	}{
		{"one EINTR", 1, unix.EINTR, true, 2},
		{"EINTR up to the last try", pollPipeTries - 1, unix.EINTR, true, pollPipeTries},
		{"EINTR at each try", pollPipeTries + 5, unix.EINTR, false, pollPipeTries},
		{"another error", 3, unix.EBADF, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			pollPipe = func(fds []unix.PollFd, timeout int) (int, error) {
				calls++
				if calls <= tc.failures {
					return -1, tc.failure
				}
				return real(fds, timeout)
			}
			if got := pipeStillHeld(r); got != tc.wantHeld {
				t.Errorf("held = %v, want %v", got, tc.wantHeld)
			}
			if calls != tc.wantCalls {
				t.Errorf("polls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}
