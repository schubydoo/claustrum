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

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

const groupTermLineTail = "exited and its output was not at its end 200ms later: what was left of its process group, if anything, was sent SIGTERM ahead of the SIGKILL"

// groupSignalSeen is one signal of the end, with the state of the leader at it.
type groupSignalSeen struct {
	sig    syscall.Signal
	leader string
	at     time.Time
}

// recordLeftGroupSignals records each signal of the end and passes it on. The group
// is the one of the test's own launcher child.
func recordLeftGroupSignals(t *testing.T) func() []groupSignalSeen {
	t.Helper()
	real := signalLeftGroup
	var mu sync.Mutex
	var seen []groupSignalSeen
	signalLeftGroup = func(pgid int, sig syscall.Signal) error {
		mu.Lock()
		seen = append(seen, groupSignalSeen{sig, procState(pgid), time.Now()})
		mu.Unlock()
		return real(pgid, sig)
	}
	t.Cleanup(func() { signalLeftGroup = real })
	return func() []groupSignalSeen {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

// procState is the state letter of pid, or "" when it is gone.
func procState(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// spawnLauncherGroup starts the helper as a launcher with the given member.
func spawnLauncherGroup(t *testing.T, m *Manager, c *conn.Conn, id, shape, kind, leader string) (*Proc, string) {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "member")
	command := filepath.Join(dir, "cli")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := helperCommand(t, "group-leader")
	launcher := []string{exe}
	p, err := m.spawnWith(c, id, command, []string{pidFile, shape, kind, leader}, "", env, false, &launcher, 0, TempRootAsk{}, nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// The exit goroutine reads the seams, so it ends before the next test sets them.
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

// memberPid reads the member's pid and kills that own child at the end of the test.
func memberPid(t *testing.T, pidFile string) int {
	t.Helper()
	pid := waitPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

func awaitProcDone(t *testing.T, p *Proc) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(20 * time.Second):
		t.Fatal("no exit frame")
	}
}

func awaitMemberGone(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if s := procState(pid); s == "" || s == "Z" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("member %d lives on", pid)
		}
	}
}

func sigsOf(seen []groupSignalSeen) []syscall.Signal {
	out := make([]syscall.Signal, len(seen))
	for i, s := range seen {
		out[i] = s.sig
	}
	return out
}

// TestLauncherGroupHeldPipesGetsTermThenKillBeforeReap: 87375cc7 on Linux. A member
// that holds the pipes gets SIGTERM after 200 ms, with the line, then SIGKILL as soon
// as the pipes end. The leader is a zombie at both signals.
func TestLauncherGroupHeldPipesGetsTermThenKillBeforeReap(t *testing.T) {
	oldBound := groupKillBound
	groupKillBound = 6 * time.Second
	t.Cleanup(func() { groupKillBound = oldBound })
	seen := recordLeftGroupSignals(t)
	buf := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, _ := rawLineConn(t)
	start := time.Now()
	p, pidFile := spawnLauncherGroup(t, m, c, "lgheld", "hold", "exit", "go")
	member := memberPid(t, pidFile)
	awaitProcDone(t, p)
	awaitMemberGone(t, member)
	got := seen()
	if !slices.Equal(sigsOf(got), []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("signals = %v, want SIGTERM, SIGKILL", got)
	}
	for _, s := range got {
		if s.leader != "Z" {
			t.Errorf("leader state at %v = %q, want the zombie Z", s.sig, s.leader)
		}
	}
	// The member that left on the SIGTERM closed the pipes, so the SIGKILL comes then.
	if d := got[1].at.Sub(got[0].at); d > 3*time.Second {
		t.Errorf("SIGKILL %v after the SIGTERM, want it at the end of the pipes", d)
	}
	if time.Since(start) < groupTermAfter {
		t.Errorf("ended before the %v wait", groupTermAfter)
	}
	if want := "[process.Manager] Process lgheld " + groupTermLineTail; !strings.Contains(buf.String(), want) {
		t.Errorf("log has no line %q", want)
	}
}

// TestClaudeCodeGroupGetsTermThenKillBeforeReap: 13047a51 on Linux ends the group of a
// Claude Code child with no launcher as the group of a launcher. A member that holds the
// pipes and ignores SIGTERM gets SIGTERM 200 ms after the exit, with the line, then the
// SIGKILL at the bound (measured at 1 to 3 s). The leader is a zombie at both signals.
func TestClaudeCodeGroupGetsTermThenKillBeforeReap(t *testing.T) {
	old := groupKillBound
	groupKillBound = 400 * time.Millisecond
	t.Cleanup(func() { groupKillBound = old })
	seen := recordLeftGroupSignals(t)
	buf := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	p, member := spawnCLIGroup(t, m, "ccheld", "ccd-cli/2.0.0", TempRootAsk{}, "hold", "ignore")
	awaitProcDone(t, p)
	awaitMemberGone(t, member)
	got := seen()
	if !slices.Equal(sigsOf(got), []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("signals = %v, want SIGTERM, SIGKILL", got)
	}
	for _, s := range got {
		if s.leader != "Z" {
			t.Errorf("leader state at %v = %q, want the zombie Z", s.sig, s.leader)
		}
	}
	if d := got[1].at.Sub(got[0].at); d < groupKillBound {
		t.Errorf("SIGKILL %v after the SIGTERM, want the %v bound", d, groupKillBound)
	}
	if want := "[process.Manager] Process ccheld " + groupTermLineTail; !strings.Contains(buf.String(), want) {
		t.Errorf("log has no line %q", want)
	}
}

// TestLauncherGroupKillAtTheBound: a member that ignores SIGTERM gets the SIGKILL at
// the bound after the SIGTERM (87375cc7, measured on Linux at 1.5 s).
func TestLauncherGroupKillAtTheBound(t *testing.T) {
	old := groupKillBound
	groupKillBound = 400 * time.Millisecond
	t.Cleanup(func() { groupKillBound = old })
	var termAt, killAt time.Time
	real := signalLeftGroup
	var mu sync.Mutex
	signalLeftGroup = func(pgid int, sig syscall.Signal) error {
		mu.Lock()
		switch sig {
		case syscall.SIGTERM:
			termAt = time.Now()
		case syscall.SIGKILL:
			killAt = time.Now()
		}
		mu.Unlock()
		return real(pgid, sig)
	}
	t.Cleanup(func() { signalLeftGroup = real })
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, _ := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, "lgbound", "hold", "ignore", "go")
	member := memberPid(t, pidFile)
	awaitProcDone(t, p)
	awaitMemberGone(t, member)
	mu.Lock()
	defer mu.Unlock()
	if termAt.IsZero() || killAt.IsZero() {
		t.Fatalf("term %v, kill %v", termAt, killAt)
	}
	if d := killAt.Sub(termAt); d < 400*time.Millisecond {
		t.Errorf("SIGKILL %v after the SIGTERM, want the 400ms bound", d)
	}
}

// TestLauncherGroupKillsAtOnce: closed pipes and no member get one SIGKILL, no SIGTERM
// and no line (87375cc7, measured on Linux).
func TestLauncherGroupKillsAtOnce(t *testing.T) {
	for _, shape := range []string{"closed", "none"} {
		t.Run(shape, func(t *testing.T) {
			seen := recordLeftGroupSignals(t)
			buf := captureLogBuf(t)
			m := newTestProcManager(t)
			t.Cleanup(m.KillAll)
			c, _ := rawLineConn(t)
			id := "lgonce" + shape
			p, pidFile := spawnLauncherGroup(t, m, c, id, shape, "ignore", "go")
			member := 0
			if shape == "closed" {
				member = memberPid(t, pidFile)
			}
			awaitProcDone(t, p)
			if member != 0 {
				awaitMemberGone(t, member)
			}
			if got := sigsOf(seen()); !slices.Equal(got, []syscall.Signal{syscall.SIGKILL}) {
				t.Errorf("signals = %v, want one SIGKILL", got)
			}
			if strings.Contains(buf.String(), "Process "+id+" "+groupTermLineTail) {
				t.Error("logged the SIGTERM line")
			}
		})
	}
}

// TestPlainSpawnGetsNoGroupEnd: a spawn with no launcher gets no signal of this end.
func TestPlainSpawnGetsNoGroupEnd(t *testing.T) {
	seen := recordLeftGroupSignals(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	exe, env := helperCommand(t, "group-leader")
	pidFile := filepath.Join(t.TempDir(), "member")
	p, err := m.spawnWith(c, "lgplain", exe, []string{"unused", pidFile, "closed", "exit", "go"}, "", env, false, nil, 0, TempRootAsk{}, nil, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	member := memberPid(t, pidFile)
	nextLine(t, lines, `"stream":"exit"`, 15*time.Second)
	awaitProcDone(t, p)
	if got := seen(); len(got) != 0 {
		t.Errorf("signals = %v, want none", got)
	}
	if s := procState(member); s == "" || s == "Z" {
		t.Errorf("member state %q, want it alive", s)
	}
}

// TestLeaveGroupsSendsNothing: with LeaveGroups a launcher's group gets no signal.
func TestLeaveGroupsSendsNothing(t *testing.T) {
	seen := recordLeftGroupSignals(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	m.LeaveGroups = true
	c, _ := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, "lgleave", "closed", "exit", "go")
	member := memberPid(t, pidFile)
	awaitProcDone(t, p)
	if got := seen(); len(got) != 0 {
		t.Errorf("signals = %v, want none", got)
	}
	if s := procState(member); s == "" || s == "Z" {
		t.Errorf("member state %q, want it alive", s)
	}
}

// TestLauncherGroupEndsAfterAProcessKill: the end follows a process.kill of the leader
// too, and the exit frame keeps its killedBy (87375cc7, measured on Linux).
func TestLauncherGroupEndsAfterAProcessKill(t *testing.T) {
	seen := recordLeftGroupSignals(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, "lgkill", "hold", "exit", "stay")
	member := memberPid(t, pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 15*time.Second)
	if err := m.Kill("lgkill", "TERM"); err != nil {
		t.Fatal(err)
	}
	exit := nextLine(t, lines, `"stream":"exit"`, 15*time.Second)
	awaitProcDone(t, p)
	awaitMemberGone(t, member)
	if !strings.Contains(exit, `"killedBy":"client"`) {
		t.Errorf("exit frame = %s, want killedBy client", exit)
	}
	if got := sigsOf(seen()); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Errorf("signals = %v, want SIGTERM, SIGKILL", got)
	}
}

// TestKillInTheGroupWaitClaimsNoExit: a process.kill while the end waits on an exited
// leader claims no killedBy (claustrum's own, as in the exit drain).
func TestKillInTheGroupWaitClaimsNoExit(t *testing.T) {
	old := groupKillBound
	groupKillBound = 800 * time.Millisecond
	t.Cleanup(func() { groupKillBound = old })
	termed := make(chan struct{})
	var once sync.Once
	real := signalLeftGroup
	signalLeftGroup = func(pgid int, sig syscall.Signal) error {
		err := real(pgid, sig)
		if sig == syscall.SIGTERM {
			once.Do(func() { close(termed) })
		}
		return err
	}
	t.Cleanup(func() { signalLeftGroup = real })
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, "lgclaim", "hold", "ignore", "go")
	memberPid(t, pidFile)
	select {
	case <-termed:
	case <-time.After(15 * time.Second):
		t.Fatal("no SIGTERM")
	}
	if err := m.Kill("lgclaim", "TERM"); err != nil {
		t.Fatal(err)
	}
	exit := nextLine(t, lines, `"stream":"exit"`, 15*time.Second)
	awaitProcDone(t, p)
	if strings.Contains(exit, "killedBy") {
		t.Errorf("exit frame = %s, want no killedBy", exit)
	}
}

// TestGroupEndAfterTheSweepKillSendsOneKill: a running launcher that the shutdown sweep
// sent SIGKILL gets one more group SIGKILL from this end, at once and before its reap,
// with no SIGTERM (87375cc7 and 13047a51, measured on Linux).
func TestGroupEndAfterTheSweepKillSendsOneKill(t *testing.T) {
	seen := recordLeftGroupSignals(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, "lgstop", "closed", "exit", "stay")
	memberPid(t, pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 15*time.Second)
	m.RefuseStarts()
	p.signalIfLive("KILL", "shutdown")
	awaitProcDone(t, p)
	got := seen()
	if !slices.Equal(sigsOf(got), []syscall.Signal{syscall.SIGKILL}) {
		t.Fatalf("signals = %v, want one SIGKILL", got)
	}
	if got[0].leader != "Z" {
		t.Errorf("leader state at the SIGKILL = %q, want Z", got[0].leader)
	}
}

// awaitLeaderZombie returns once the leader has exited and is not reaped.
func awaitLeaderZombie(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); procState(pid) != "Z"; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("leader %d state %q, want Z", pid, procState(pid))
		}
	}
}

// shortDrainForTest makes the exit frame of a held pipe come soon after the end.
func shortDrainForTest(t *testing.T) {
	t.Helper()
	old := SetExitDrainGraceForTest(200 * time.Millisecond)
	t.Cleanup(func() { SetExitDrainGraceForTest(old) })
}

// startHeldForShutdown starts a launcher whose member ignores SIGTERM and holds the
// pipes, and returns once the leader is a held zombie.
func startHeldForShutdown(t *testing.T, id string, keep bool, term time.Duration) (*Manager, *Proc, int, func() []groupSignalSeen) {
	t.Helper()
	shortDrainForTest(t)
	oldTerm, oldBound := groupTermAfter, groupKillBound
	groupTermAfter, groupKillBound = term, 8*time.Second
	t.Cleanup(func() { groupTermAfter, groupKillBound = oldTerm, oldBound })
	seen := recordLeftGroupSignals(t)
	m := newTestProcManager(t)
	m.KeepChildren = keep
	t.Cleanup(m.KillAll)
	c, _ := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, id, "hold", "ignore", "go")
	member := memberPid(t, pidFile)
	awaitLeaderZombie(t, p.pid)
	return m, p, member, seen
}

// awaitHeldSignal returns once the end sent sig.
func awaitHeldSignal(t *testing.T, seen func() []groupSignalSeen, sig syscall.Signal) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !slices.Contains(sigsOf(seen()), sig); time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no %v, signals = %v", sig, seen())
		}
	}
}

// TestShutdownBeforeTheTermKillsTheGroup: a shutdown in the 200 ms wait gets one group
// SIGKILL at once, with no SIGTERM, while the leader is a zombie (87375cc7, measured).
func TestShutdownBeforeTheTermKillsTheGroup(t *testing.T) {
	m, p, member, seen := startHeldForShutdown(t, "lgstopa", false, 3*time.Second)
	m.RefuseStarts()
	awaitHeldSignal(t, seen, syscall.SIGKILL)
	awaitMemberGone(t, member)
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the end still waits after the shutdown began")
	}
	got := seen()
	if !slices.Equal(sigsOf(got), []syscall.Signal{syscall.SIGKILL}) {
		t.Fatalf("signals = %v, want one SIGKILL", got)
	}
	if got[0].leader != "Z" {
		t.Errorf("leader state at the SIGKILL = %q, want Z", got[0].leader)
	}
}

// TestShutdownAfterTheTermKillsTheGroup: a shutdown after the group SIGTERM brings the
// SIGKILL at the shutdown, not at the bound (87375cc7, measured on Linux).
func TestShutdownAfterTheTermKillsTheGroup(t *testing.T) {
	m, p, member, seen := startHeldForShutdown(t, "lgstopb", false, 50*time.Millisecond)
	awaitHeldSignal(t, seen, syscall.SIGTERM)
	m.RefuseStarts()
	awaitHeldSignal(t, seen, syscall.SIGKILL)
	awaitMemberGone(t, member)
	awaitProcDone(t, p)
	got := seen()
	if !slices.Equal(sigsOf(got), []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("signals = %v, want SIGTERM, SIGKILL", got)
	}
	if d := got[1].at.Sub(got[0].at); d > 4*time.Second {
		t.Errorf("SIGKILL %v after the SIGTERM, want it at the shutdown", d)
	}
	if got[1].leader != "Z" {
		t.Errorf("leader state at the SIGKILL = %q, want Z", got[1].leader)
	}
}

// TestKeepChildrenShutdownSendsNothing: with -keep-children a shutdown in the wait sends
// no signal and the member lives (claustrum's own).
func TestKeepChildrenShutdownSendsNothing(t *testing.T) {
	t.Run("before the SIGTERM", func(t *testing.T) {
		m, p, member, seen := startHeldForShutdown(t, "lgkeepa", true, time.Second)
		m.RefuseStarts()
		awaitProcDone(t, p)
		if got := seen(); len(got) != 0 {
			t.Errorf("signals = %v, want none", got)
		}
		if s := procState(member); s == "" || s == "Z" {
			t.Errorf("member state %q, want it alive", s)
		}
	})
	t.Run("after the SIGTERM", func(t *testing.T) {
		m, p, member, seen := startHeldForShutdown(t, "lgkeepb", true, 50*time.Millisecond)
		awaitHeldSignal(t, seen, syscall.SIGTERM)
		m.RefuseStarts()
		select {
		case <-p.done:
		case <-time.After(4 * time.Second):
			t.Fatal("the end still waits after the shutdown began")
		}
		if got := sigsOf(seen()); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM}) {
			t.Errorf("signals = %v, want the SIGTERM alone", got)
		}
		if s := procState(member); s == "" || s == "Z" {
			t.Errorf("member state %q, want it alive", s)
		}
	})
}

// TestShutdownSweepCountsTheHeldLauncher: the sweep counts the group of a held leader
// as ended, the group gets one SIGKILL, and the record step runs (87375cc7, measured).
func TestShutdownSweepCountsTheHeldLauncher(t *testing.T) {
	m, p, member, seen := startHeldForShutdown(t, "lgsweep", false, 3*time.Second)
	// Before the end holds the leader, the sweep kills the group by its usual signal.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		held := p.leaderHeld
		p.mu.Unlock()
		if held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the end never held the leader")
		}
	}
	m.StopStarts()
	if n := m.KillAllCount(); n != 1 {
		t.Errorf("KillAllCount = %d, want 1", n)
	}
	awaitMemberGone(t, member)
	select {
	case <-p.recordDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the record step did not run")
	}
	awaitProcDone(t, p)
	if got := sigsOf(seen()); !slices.Equal(got, []syscall.Signal{syscall.SIGKILL}) {
		t.Errorf("signals = %v, want one SIGKILL", got)
	}
}

// TestKillInTheGroupWaitEndsTheGroupAtOnce: a process.kill KILL while the end holds the
// zombie leader kills the group at once, as in the exit drain (measured rule of the
// drain). The bound is far above the limit of the look at the member.
func TestKillInTheGroupWaitEndsTheGroupAtOnce(t *testing.T) {
	oldBound := groupKillBound
	groupKillBound = 30 * time.Second
	t.Cleanup(func() { groupKillBound = oldBound })
	termed := make(chan struct{})
	var once sync.Once
	real := signalLeftGroup
	signalLeftGroup = func(pgid int, sig syscall.Signal) error {
		err := real(pgid, sig)
		if sig == syscall.SIGTERM {
			once.Do(func() { close(termed) })
		}
		return err
	}
	t.Cleanup(func() { signalLeftGroup = real })
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, _ := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, "lgkillnow", "hold", "ignore", "go")
	member := memberPid(t, pidFile)
	select {
	case <-termed:
	case <-time.After(15 * time.Second):
		t.Fatal("no SIGTERM")
	}
	if err := m.Kill("lgkillnow", "KILL"); err != nil {
		t.Fatal(err)
	}
	awaitMemberGone(t, member)
	awaitProcDone(t, p)
}

// TestLeaderHeldClearsBeforeTheReap: the end returns with leaderHeld false while the
// leader is still an unreaped zombie. So the skip of the pid test never covers a
// reaped leader.
func TestLeaderHeldClearsBeforeTheReap(t *testing.T) {
	real := signalLeftGroup
	heldAtKill := false
	var p *Proc
	signalLeftGroup = func(_ int, sig syscall.Signal) error {
		// The end sends its SIGKILL under p.mu.
		if sig == syscall.SIGKILL {
			heldAtKill = p.leaderHeld
		}
		return nil
	}
	t.Cleanup(func() { signalLeftGroup = real })
	exe, env := helperCommand(t, "group-leader")
	cmd := exec.Command(exe, "cli", "unused", "none", "exit", "go")
	cmd.Env = BuildEnv(env)
	cmd.SysProcAttr = sysproc.NewSysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p = &Proc{id: "lgheld2", cmd: cmd, stopsBegan: &atomic.Bool{}}
	drained := make(chan struct{})
	close(drained)
	p.settleLeftGroupHeld(drained)
	state := procState(cmd.Process.Pid)
	p.mu.Lock()
	held := p.leaderHeld
	p.mu.Unlock()
	_ = cmd.Wait()
	if !heldAtKill {
		t.Error("leaderHeld was false at the group SIGKILL")
	}
	if held {
		t.Error("leaderHeld is still true when the end returns, before the reap")
	}
	if state != "Z" {
		t.Errorf("leader state after the end = %q, want the unreaped Z", state)
	}
}

// TestLauncherGroupPipeHolderInOwnSession: a holder of the pipes in its own session, with
// no member in the group, still brings the SIGTERM and the line. The SIGKILL comes at
// the bound, as the pipes stay open (87375cc7, measured on Linux).
func TestLauncherGroupPipeHolderInOwnSession(t *testing.T) {
	shortDrainForTest(t)
	oldBound := groupKillBound
	groupKillBound = 500 * time.Millisecond
	t.Cleanup(func() { groupKillBound = oldBound })
	seen := recordLeftGroupSignals(t)
	buf := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, _ := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, "lghold", "hold", "holder", "go")
	holder := memberPid(t, pidFile)
	awaitProcDone(t, p)
	got := seen()
	if !slices.Equal(sigsOf(got), []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("signals = %v, want SIGTERM, SIGKILL", got)
	}
	if d := got[1].at.Sub(got[0].at); d < 450*time.Millisecond {
		t.Errorf("SIGKILL %v after the SIGTERM, want the 500ms bound", d)
	}
	if want := "[process.Manager] Process lghold " + groupTermLineTail; !strings.Contains(buf.String(), want) {
		t.Errorf("log has no line %q", want)
	}
	if s := procState(holder); s == "" || s == "Z" {
		t.Errorf("holder state %q, want it alive outside the group", s)
	}
}

// TestGroupTermCountsFromTheSeenExit: work after the exit is seen does not move the
// SIGTERM. The test holds p.mu past the seen exit for 700 ms of a 1 s wait.
func TestGroupTermCountsFromTheSeenExit(t *testing.T) {
	oldTerm, oldBound := groupTermAfter, groupKillBound
	groupTermAfter, groupKillBound = time.Second, 50*time.Millisecond
	t.Cleanup(func() { groupTermAfter, groupKillBound = oldTerm, oldBound })
	real := signalLeftGroup
	var mu sync.Mutex
	var termAt time.Time
	signalLeftGroup = func(_ int, sig syscall.Signal) error {
		if sig == syscall.SIGTERM {
			mu.Lock()
			termAt = time.Now()
			mu.Unlock()
		}
		return nil
	}
	t.Cleanup(func() { signalLeftGroup = real })
	exe, env := helperCommand(t, "group-leader")
	cmd := exec.Command(exe, "cli", "unused", "none", "exit", "go")
	cmd.Env = BuildEnv(env)
	cmd.SysProcAttr = sysproc.NewSysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &Proc{id: "lgtime", cmd: cmd, stopsBegan: &atomic.Bool{}}
	p.mu.Lock()
	done := make(chan struct{})
	go func() { p.settleLeftGroupHeld(make(chan struct{})); close(done) }()
	awaitLeaderZombie(t, cmd.Process.Pid)
	exited := time.Now()
	time.Sleep(700 * time.Millisecond)
	p.mu.Unlock()
	<-done
	_ = cmd.Wait()
	mu.Lock()
	defer mu.Unlock()
	if termAt.IsZero() {
		t.Fatal("no SIGTERM")
	}
	if d := termAt.Sub(exited); d >= 1500*time.Millisecond {
		t.Errorf("SIGTERM %v after the exit, want about 1s", d)
	}
}

// TestLauncherDrainCountsFromTheLeaderExit: the drain grace of a launcher counts from
// the exit of the leader, not from the reap after the group end (87375cc7, measured on
// Linux at 5.0 s with the reap at 1.7 s).
func TestLauncherDrainCountsFromTheLeaderExit(t *testing.T) {
	oldGrace := SetExitDrainGraceForTest(2 * time.Second)
	t.Cleanup(func() { SetExitDrainGraceForTest(oldGrace) })
	oldBound := groupKillBound
	groupKillBound = time.Second
	t.Cleanup(func() { groupKillBound = oldBound })
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, _ := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, "lgdrain", "hold", "holder", "go")
	memberPid(t, pidFile)
	awaitLeaderZombie(t, p.pid)
	exited := time.Now()
	awaitProcDone(t, p)
	d := time.Since(exited)
	if d < 1800*time.Millisecond || d >= 2700*time.Millisecond {
		t.Errorf("exit frame %v after the leader's exit, want about 2s", d)
	}
}

// TestPlainDrainKeepsItsWholeGrace: a spawn with no launcher waits its whole grace
// after its exit.
func TestPlainDrainKeepsItsWholeGrace(t *testing.T) {
	oldGrace := SetExitDrainGraceForTest(1500 * time.Millisecond)
	t.Cleanup(func() { SetExitDrainGraceForTest(oldGrace) })
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, _ := rawLineConn(t)
	exe, env := helperCommand(t, "group-leader")
	pidFile := filepath.Join(t.TempDir(), "member")
	p, err := m.spawnWith(c, "lgplaind", exe, []string{"unused", pidFile, "hold", "holder", "go"}, "", env, false, nil, 0, TempRootAsk{}, nil, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	memberPid(t, pidFile)
	start := time.Now()
	awaitProcDone(t, p)
	if d := time.Since(start); d < time.Second {
		t.Errorf("exit frame %v after the holder started, want the 1.5s grace", d)
	}
}

// TestAskedLauncherPipesAtEndGetsTheKill: a stream-json launcher that the sweep asked to
// end, with its pipes at their end, gets one group SIGKILL while it is a zombie, and a
// member with its own stdio ends (87375cc7, measured on Linux).
func TestAskedLauncherPipesAtEndGetsTheKill(t *testing.T) {
	seen := recordLeftGroupSignals(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	p, pidFile := spawnAskedLauncher(t, m, c, "lgaskc", "closed")
	member := memberPid(t, pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 15*time.Second)
	m.StopStarts()
	m.KillAllCount()
	awaitProcDone(t, p)
	awaitMemberGone(t, member)
	got := seen()
	if !slices.Equal(sigsOf(got), []syscall.Signal{syscall.SIGKILL}) {
		t.Fatalf("signals = %v, want one SIGKILL", got)
	}
	if got[0].leader != "Z" {
		t.Errorf("leader state at the SIGKILL = %q, want Z", got[0].leader)
	}
}

// TestAskedLauncherHeldPipesGetsTermThenKill: with its pipes held, the asked launcher's
// group gets SIGTERM, the line and SIGKILL at the bound (87375cc7, measured on Linux).
func TestAskedLauncherHeldPipesGetsTermThenKill(t *testing.T) {
	shortDrainForTest(t)
	oldBound, oldGrace := groupKillBound, shutdownEndGrace
	groupKillBound, shutdownEndGrace = 400*time.Millisecond, 10*time.Second
	t.Cleanup(func() { groupKillBound, shutdownEndGrace = oldBound, oldGrace })
	seen := recordLeftGroupSignals(t)
	buf := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	p, pidFile := spawnAskedLauncher(t, m, c, "lgaskh", "hold")
	member := memberPid(t, pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 15*time.Second)
	m.StopStarts()
	m.KillAllCount()
	awaitProcDone(t, p)
	awaitMemberGone(t, member)
	got := seen()
	if !slices.Equal(sigsOf(got), []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("signals = %v, want SIGTERM, SIGKILL", got)
	}
	if d := got[1].at.Sub(got[0].at); d < 350*time.Millisecond {
		t.Errorf("SIGKILL %v after the SIGTERM, want the 400ms bound", d)
	}
	if want := "[process.Manager] Process lgaskh " + groupTermLineTail; !strings.Contains(buf.String(), want) {
		t.Errorf("log has no line %q", want)
	}
}

// TestAskedLauncherStopsAtTheSweepKill: the group SIGKILL of the end of the sweep's wait
// ends this end, with no SIGKILL of its own and no wait for its bound.
func TestAskedLauncherStopsAtTheSweepKill(t *testing.T) {
	shortDrainForTest(t)
	oldBound, oldGrace := groupKillBound, shutdownEndGrace
	groupKillBound, shutdownEndGrace = 8*time.Second, 600*time.Millisecond
	t.Cleanup(func() { groupKillBound, shutdownEndGrace = oldBound, oldGrace })
	seen := recordLeftGroupSignals(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	p, pidFile := spawnAskedLauncher(t, m, c, "lgasks", "hold")
	member := memberPid(t, pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 15*time.Second)
	m.StopStarts()
	m.KillAllCount()
	awaitMemberGone(t, member)
	select {
	case <-p.done:
	case <-time.After(4 * time.Second):
		t.Fatal("the end still waits after the group SIGKILL of the sweep")
	}
	if got := sigsOf(seen()); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM}) {
		t.Errorf("signals of this end = %v, want the SIGTERM alone", got)
	}
}

// TestAwaitPipesEndReadsAClosedDrainAtTheBound: with a closed drain and an expired
// bound, the answer is always that the pipes are at their end.
func TestAwaitPipesEndReadsAClosedDrainAtTheBound(t *testing.T) {
	drained := make(chan struct{})
	close(drained)
	for i := range 5000 {
		if !awaitPipesEnd(drained, -time.Second, func() bool { return false }) {
			t.Fatalf("round %d: a closed drain read as open", i)
		}
	}
}

// TestAskedLauncherOneKillWhenTheEndKillsFirst: the end sends the SIGKILL at its bound,
// and endAsked adds none to the group number at its later timer.
func TestAskedLauncherOneKillWhenTheEndKillsFirst(t *testing.T) {
	oldDrain := SetExitDrainGraceForTest(2500 * time.Millisecond)
	oldBound, oldGrace := groupKillBound, shutdownEndGrace
	groupKillBound, shutdownEndGrace = 100*time.Millisecond, 1200*time.Millisecond
	t.Cleanup(func() {
		SetExitDrainGraceForTest(oldDrain)
		groupKillBound, shutdownEndGrace = oldBound, oldGrace
	})
	var cur atomic.Pointer[Proc]
	kills, late := recordGroupKills(t, &cur)
	buf := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	p, pidFile := spawnAskedLauncher(t, m, c, "lgfirst", "two")
	cur.Store(p)
	memberPid(t, pidFile)
	memberPid(t, pidFile+".holder")
	nextLine(t, lines, `"stream":"stdout"`, 15*time.Second)
	m.StopStarts()
	m.KillAllCount()
	awaitProcDone(t, p)
	if n := kills.Load(); n != 1 {
		t.Errorf("group SIGKILLs = %d, want 1", n)
	}
	if n := late.Load(); n != 0 {
		t.Errorf("%d group SIGKILL after the exit frame", n)
	}
	if want := "1 exited, 0 had to be killed"; !strings.Contains(buf.String(), want) {
		t.Errorf("log has no %q", want)
	}
}

// TestEndAskedClaimLandsBeforeTheReap: a SIGKILL that endAsked claims while the end
// holds the leader reaches a zombie, and the leader is reaped only after it.
func TestEndAskedClaimLandsBeforeTheReap(t *testing.T) {
	oldDrain := SetExitDrainGraceForTest(500 * time.Millisecond)
	oldBound, oldGrace := groupKillBound, shutdownEndGrace
	groupKillBound, shutdownEndGrace = 8*time.Second, 500*time.Millisecond
	t.Cleanup(func() {
		SetExitDrainGraceForTest(oldDrain)
		groupKillBound, shutdownEndGrace = oldBound, oldGrace
	})
	realGroup := signalGroup
	var mu sync.Mutex
	var states []string
	signalGroup = func(g *ProcGroup, proc *os.Process, name string) error {
		if name == "KILL" {
			mu.Lock()
			states = append(states, procState(proc.Pid))
			mu.Unlock()
		}
		return realGroup(g, proc, name)
	}
	t.Cleanup(func() { signalGroup = realGroup })
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	p, pidFile := spawnAskedLauncher(t, m, c, "lgclaim2", "hold")
	memberPid(t, pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 15*time.Second)
	m.StopStarts()
	m.KillAllCount()
	awaitProcDone(t, p)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(states, []string{"Z"}) {
		t.Errorf("leader state at each group SIGKILL of endAsked = %q, want one Z", states)
	}
}

// startExitedLeader starts a helper that exits at once, in its own group, as a Proc of
// a launcher. The test reaps it.
func startExitedLeader(t *testing.T, id string) *Proc {
	t.Helper()
	exe, env := helperCommand(t, "group-leader")
	cmd := exec.Command(exe, "cli", "unused", "none", "exit", "go")
	cmd.Env = BuildEnv(env)
	cmd.SysProcAttr = sysproc.NewSysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return &Proc{id: id, cmd: cmd, running: true, endsGroup: true, stopsBegan: &atomic.Bool{}}
}

// TestLinuxEndSendsNoKillAfterAClaim: with the claim taken, the end sends no SIGKILL.
func TestLinuxEndSendsNoKillAfterAClaim(t *testing.T) {
	sent := fakeLeftGroup(t, nil)
	p := startExitedLeader(t, "lgtaken")
	p.groupKillClaimed.Store(true)
	drained := make(chan struct{})
	close(drained)
	p.settleLeftGroupHeld(drained)
	_ = p.cmd.Wait()
	if got := sent(); len(got) != 0 {
		t.Errorf("signals = %v, want none", got)
	}
}

// TestLinuxEndClaimsAGroupThatAnotherEndKilled: after the unattended end killed the
// group, the end takes the claim, so endAsked adds no SIGKILL.
func TestLinuxEndClaimsAGroupThatAnotherEndKilled(t *testing.T) {
	sent := fakeLeftGroup(t, nil)
	p := startExitedLeader(t, "lgother")
	p.unheldKilled.Store(true)
	p.settleLeftGroupHeld(make(chan struct{}))
	_ = p.cmd.Wait()
	if got := sent(); len(got) != 0 {
		t.Errorf("signals = %v, want none", got)
	}
	if !p.groupKillClaimed.Load() {
		t.Error("the claim is free after another end killed the group")
	}
}

// TestEndWaitsForAClaimedKillBeforeItsReap: the end that lost the claim cannot return to
// its reap while endAsked still sends the SIGKILL under p.mu.
func TestEndWaitsForAClaimedKillBeforeItsReap(t *testing.T) {
	oldTerm := groupTermAfter
	groupTermAfter = 30 * time.Second
	t.Cleanup(func() { groupTermAfter = oldTerm })
	fakeLeftGroup(t, nil)
	p := startExitedLeader(t, "lgorder")
	drained := make(chan struct{})
	returned := make(chan struct{})
	go func() { p.settleLeftGroupHeld(drained); close(returned) }()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		held := p.leaderHeld
		p.mu.Unlock()
		if held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the end never held the leader")
		}
	}
	realGroup := signalGroup
	early := false
	signalGroup = func(*ProcGroup, *os.Process, string) error {
		close(drained)
		select {
		case <-returned:
			early = true
		case <-time.After(300 * time.Millisecond):
		}
		return nil
	}
	t.Cleanup(func() { signalGroup = realGroup })
	p.killGroupBeforeExitFrame()
	<-returned
	_ = p.cmd.Wait()
	if early {
		t.Error("the end went on to its reap while the claimed SIGKILL was in flight")
	}
}

// awaitLeaderHeld returns once the end holds the leader.
func awaitLeaderHeld(t *testing.T, p *Proc) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		held := p.leaderHeld
		p.mu.Unlock()
		if held {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the end never held the leader")
		}
	}
}

// TestClientKillInTheHoldIsTheOneKill: a process.kill KILL in the hold ends the group at
// once while the zombie holds the number. The end then sends no signal and stops waiting.
func TestClientKillInTheHoldIsTheOneKill(t *testing.T) {
	m, p, member, seen := startHeldForShutdown(t, "lgclient", false, 2*time.Second)
	realGroup := signalGroup
	var mu sync.Mutex
	var states []string
	signalGroup = func(g *ProcGroup, proc *os.Process, name string) error {
		if name == "KILL" {
			mu.Lock()
			states = append(states, procState(proc.Pid))
			mu.Unlock()
		}
		return realGroup(g, proc, name)
	}
	t.Cleanup(func() { signalGroup = realGroup })
	awaitLeaderHeld(t, p)
	if err := m.Kill("lgclient", "KILL"); err != nil {
		t.Fatal(err)
	}
	awaitMemberGone(t, member)
	select {
	case <-p.done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("the end still waits after the client KILL")
	}
	if got := seen(); len(got) != 0 {
		t.Errorf("signals of the end = %v, want none", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(states, []string{"Z"}) {
		t.Errorf("leader state at each client SIGKILL = %q, want one Z", states)
	}
}

// TestClientKillWithAHolderEndsTheWait: after a client KILL in the hold, a holder in its
// own session keeps the pipes open. The end sends no SIGTERM and does not wait its bound.
func TestClientKillWithAHolderEndsTheWait(t *testing.T) {
	shortDrainForTest(t)
	oldTerm, oldBound := groupTermAfter, groupKillBound
	groupTermAfter, groupKillBound = 500*time.Millisecond, 8*time.Second
	t.Cleanup(func() { groupTermAfter, groupKillBound = oldTerm, oldBound })
	seen := recordLeftGroupSignals(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, _ := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, "lgholdk", "two", "ignore", "go")
	memberPid(t, pidFile)
	memberPid(t, pidFile+".holder")
	awaitLeaderHeld(t, p)
	if err := m.Kill("lgholdk", "KILL"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(4 * time.Second):
		t.Fatal("the end still waits after the client KILL")
	}
	if got := seen(); len(got) != 0 {
		t.Errorf("signals of the end = %v, want none", got)
	}
}

// TestRunningEscalationKeepsItsKillAndTheEnd: an escalation on a launcher that still runs
// sends each time and takes no claim, so the end after the exit still sends its SIGKILL.
func TestRunningEscalationKeepsItsKillAndTheEnd(t *testing.T) {
	seen := recordLeftGroupSignals(t)
	realGroup := signalGroup
	var fake atomic.Bool
	var sent atomic.Int32
	fake.Store(true)
	signalGroup = func(g *ProcGroup, proc *os.Process, name string) error {
		if fake.Load() {
			sent.Add(1)
			return nil
		}
		return realGroup(g, proc, name)
	}
	t.Cleanup(func() { signalGroup = realGroup })
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, "lgrun", "closed", "ignore", "stay")
	member := memberPid(t, pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 15*time.Second)
	for range 2 {
		if err := p.killGroupAfterExit(); err != nil {
			t.Fatal(err)
		}
	}
	if n := sent.Load(); n != 2 {
		t.Errorf("escalations on a running leader sent %d, want 2", n)
	}
	if p.groupKillClaimed.Load() {
		t.Fatal("an escalation on a running leader took the claim")
	}
	// A taken claim means that the exit came, also if the pid shows a running process.
	p.groupKillClaimed.Store(true)
	if err := p.killGroupAfterExit(); err != nil {
		t.Fatal(err)
	}
	if n := sent.Load(); n != 2 {
		t.Errorf("with the claim taken: %d signals in all, want 2", n)
	}
	p.groupKillClaimed.Store(false)
	// A reaped mark means the same, so this escalation takes the claim.
	p.mu.Lock()
	p.reaped = true
	p.mu.Unlock()
	if err := p.killGroupAfterExit(); err != nil {
		t.Fatal(err)
	}
	if !p.groupKillClaimed.Load() {
		t.Error("an escalation after the reaped mark took no claim")
	}
	p.groupKillClaimed.Store(false)
	p.mu.Lock()
	p.reaped = false
	p.mu.Unlock()
	fake.Store(false)
	if err := p.killGroupAfterExit(); err != nil {
		t.Fatal(err)
	}
	awaitProcDone(t, p)
	awaitMemberGone(t, member)
	got := seen()
	if !slices.Equal(sigsOf(got), []syscall.Signal{syscall.SIGKILL}) {
		t.Fatalf("signals of the end = %v, want its one SIGKILL", got)
	}
	if got[0].leader != "Z" {
		t.Errorf("leader state at the SIGKILL of the end = %q, want Z", got[0].leader)
	}
}

// TestEscalationAfterTheEndSendsNoSecondKill: after the end killed the group and reaped
// the leader, a holder keeps the drain open. A KILL or an escalation there sends nothing.
func TestEscalationAfterTheEndSendsNoSecondKill(t *testing.T) {
	oldDrain := SetExitDrainGraceForTest(1500 * time.Millisecond)
	oldBound := groupKillBound
	groupKillBound = 50 * time.Millisecond
	t.Cleanup(func() {
		SetExitDrainGraceForTest(oldDrain)
		groupKillBound = oldBound
	})
	var cur atomic.Pointer[Proc]
	kills, late := recordGroupKills(t, &cur)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, _ := rawLineConn(t)
	p, pidFile := spawnLauncherGroup(t, m, c, "lgagain", "two", "ignore", "go")
	cur.Store(p)
	memberPid(t, pidFile)
	memberPid(t, pidFile+".holder")
	for deadline := time.Now().Add(10 * time.Second); procState(p.pid) != ""; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the leader was not reaped")
		}
	}
	if err := p.killGroupAfterExit(); err != nil {
		t.Fatal(err)
	}
	_ = p.killGroupInDrain(false)
	if err := m.Kill("lgagain", "KILL"); err != nil {
		t.Fatal(err)
	}
	awaitProcDone(t, p)
	if n := kills.Load(); n != 1 {
		t.Errorf("group SIGKILLs = %d, want 1", n)
	}
	if n := late.Load(); n != 0 {
		t.Errorf("%d group SIGKILL after the exit frame", n)
	}
}

// TestEndSendsTheKillAfterARefusedClaim: a client KILL in the hold that the kernel refused
// gives the claim back, and the end still sends the group SIGKILL.
func TestEndSendsTheKillAfterARefusedClaim(t *testing.T) {
	oldTerm := groupTermAfter
	groupTermAfter = 30 * time.Second
	t.Cleanup(func() { groupTermAfter = oldTerm })
	sent := fakeLeftGroup(t, nil)
	drained := make(chan struct{})
	realGroup := signalGroup
	// The end reaches its claim while this refused kill is still under way.
	signalGroup = func(*ProcGroup, *os.Process, string) error {
		close(drained)
		time.Sleep(150 * time.Millisecond)
		return &SignalFailed{Name: "killed", Err: syscall.EPERM}
	}
	t.Cleanup(func() { signalGroup = realGroup })
	p := startExitedLeader(t, "lgrefuse")
	returned := make(chan struct{})
	go func() { p.settleLeftGroupHeld(drained); close(returned) }()
	awaitLeaderHeld(t, p)
	_ = p.killGroupInDrain(false)
	<-returned
	_ = p.cmd.Wait()
	if got := sent(); !slices.Equal(got, []syscall.Signal{syscall.SIGKILL}) {
		t.Errorf("signals of the end = %v, want one SIGKILL", got)
	}
}

// TestEndClaimsAGroupThatTheSweepKilled: after a group SIGKILL of the sweep, the end
// sends one SIGKILL at once and takes the claim, so a later KILL in the drain adds none.
// With -keep-children it sends nothing. The kill call is a fake.
func TestEndClaimsAGroupThatTheSweepKilled(t *testing.T) {
	realGroup := signalGroup
	var later atomic.Int32
	signalGroup = func(*ProcGroup, *os.Process, string) error { later.Add(1); return nil }
	t.Cleanup(func() { signalGroup = realGroup })
	// taken is the claim of the 1.5 s kill. The sweep KILL of a running leader takes none.
	for _, tc := range []struct{ keep, taken bool }{{false, true}, {true, true}, {false, false}} {
		keep := tc.keep
		later.Store(0)
		sent := fakeLeftGroup(t, nil)
		p := startExitedLeader(t, "lgswept")
		p.keepsGroup = keep
		p.endedByShutdown = true
		p.sweepKilledGroup.Store(true)
		if tc.taken {
			p.groupKillClaimed.tryTake()
		}
		p.settleLeftGroupHeld(make(chan struct{}))
		_ = p.cmd.Wait()
		want := []syscall.Signal{syscall.SIGKILL}
		if keep {
			want = nil
		}
		if got := sent(); !slices.Equal(got, want) {
			t.Errorf("keep %v: signals = %v, want %v", keep, got, want)
		}
		if !p.groupKillClaimed.spent() {
			t.Errorf("keep %v: the claim is not spent after the sweep killed the group", keep)
		}
		// A later group kill of the exited child sends no third SIGKILL.
		if err := p.killGroupAfterExit(); err != nil {
			t.Fatal(err)
		}
		if n := later.Load(); n != 0 {
			t.Errorf("keep %v: %d later group SIGKILL, want none", keep, n)
		}
	}
}

// TestAskedEndSendsTheKillAfterARefusedShutdownKill: the end of a launcher that the sweep
// asked to end still sends the group SIGKILL after endAsked's kill was refused. The end
// reaches its claim while that kill is under way.
func TestAskedEndSendsTheKillAfterARefusedShutdownKill(t *testing.T) {
	oldTerm := groupTermAfter
	groupTermAfter = 30 * time.Second
	t.Cleanup(func() { groupTermAfter = oldTerm })
	sent := fakeLeftGroup(t, nil)
	drained := make(chan struct{})
	realGroup := signalGroup
	signalGroup = func(*ProcGroup, *os.Process, string) error {
		close(drained)
		time.Sleep(150 * time.Millisecond)
		return &SignalFailed{Name: "killed", Err: syscall.EPERM}
	}
	t.Cleanup(func() { signalGroup = realGroup })
	p := startExitedLeader(t, "lgaskref")
	p.endedByShutdown = true
	returned := make(chan struct{})
	go func() { p.settleLeftGroupHeld(drained); close(returned) }()
	awaitLeaderHeld(t, p)
	p.killGroupBeforeExitFrame()
	<-returned
	_ = p.cmd.Wait()
	if got := sent(); !slices.Equal(got, []syscall.Signal{syscall.SIGKILL}) {
		t.Errorf("signals of the end = %v, want one SIGKILL", got)
	}
}

// TestHeldEndSkipsALeaderWithNoOwnGroup: a leader that did not make its own group gets
// no signal of the end. The kill call is a fake, and the child is the test's own.
func TestHeldEndSkipsALeaderWithNoOwnGroup(t *testing.T) {
	sent := fakeLeftGroup(t, nil)
	exe, env := helperCommand(t, "group-leader")
	cmd := exec.Command(exe, "cli", "unused", "none", "exit", "go")
	cmd.Env = BuildEnv(env)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &Proc{id: "lgnogroup", cmd: cmd, running: true, endsGroup: true, stopsBegan: &atomic.Bool{}}
	drained := make(chan struct{})
	close(drained)
	p.settleLeftGroupHeld(drained)
	_ = cmd.Wait()
	if got := sent(); len(got) != 0 {
		t.Errorf("signals = %v, want none", got)
	}
}
