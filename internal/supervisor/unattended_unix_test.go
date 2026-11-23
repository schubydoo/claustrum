//go:build unix

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

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// spawnUnheldHelper starts the helper in mode with args, armed with one minute on a
// raw line connection, and returns the process and its lines.
func spawnUnheldHelper(t *testing.T, m *Manager, id, mode string, args ...string) (*Proc, <-chan string) {
	t.Helper()
	c, lines := rawLineConn(t)
	exe, env := helperCommand(t, mode)
	p, err := m.SpawnWithTempRoot(c, id, exe, args, "", env, false, nil, 0, TempRootAsk{}, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { endOwnChild(t, p) })
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	m.DetachConn(c)
	return p, lines
}

// endUnheldNow hands the check of p one step at the end of its time.
func endUnheldNow(t *testing.T, f *unheldTicks, p *Proc) {
	t.Helper()
	f.step(t, unheldSinceOf(p).Add(p.unheldTTL))
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the child did not end")
	}
}

// TestUnheldEndOfAChildThatLeaves: a child that ends on the SIGTERM gets the SIGKILL of
// its group after it left, and a process in its group ends with it. The left line
// comes before the exited line (87375cc7, measured on Linux and macOS).
// No SIGHUP goes out. The grandchild is the test's own.
func TestUnheldEndOfAChildThatLeaves(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeUnheldTicker(t)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	pidFile := filepath.Join(t.TempDir(), "kid")
	p, _ := spawnUnheldHelper(t, m, "al-left", "term-exit0-kid", pidFile)
	<-f.steps
	kid := waitPIDFile(t, pidFile)
	gone := false
	t.Cleanup(func() {
		if !gone {
			_ = syscall.Kill(kid, syscall.SIGKILL)
		}
	})
	endUnheldNow(t, f, p)
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM", "KILL"}) {
		t.Errorf("signals = %q, want TERM, then the group KILL", got)
	}
	wantLinesInOrder(t, buf.String(),
		"[process.Manager] Process al-left: no connection attached for 1m0s: it left at SIGTERM",
		"[process.Manager] Process al-left exited with code 0, asked to end by this daemon (unattended)")
	for deadline := time.Now().Add(5 * time.Second); !gone; time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(kid, 0) != nil {
			gone = true
		} else if time.Now().After(deadline) {
			t.Fatalf("the process %d in the group lives on", kid)
		}
	}
}

// TestUnheldEndOfAChildThatStays: a child that ignores the SIGTERM gets the group SIGKILL
// at the end of the wait. The stayed line comes before the exited line (87375cc7,
// measured on Linux and macOS). A reattach gets the exit frame with the killedBy
// "unattended" (measured on Linux, macOS and Windows). The wait is shrunk here.
func TestUnheldEndOfAChildThatStays(t *testing.T) {
	old := unheldEndGrace
	unheldEndGrace = 200 * time.Millisecond
	t.Cleanup(func() { unheldEndGrace = old })
	buf := captureLogBuf(t)
	f := fakeUnheldTicker(t)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	p, _ := spawnUnheldHelper(t, m, "al-stay", "ignore-term")
	<-f.steps
	endUnheldNow(t, f, p)
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM", "KILL"}) {
		t.Errorf("signals = %q, want TERM, then the group KILL", got)
	}
	wantLinesInOrder(t, buf.String(),
		"[process.Manager] Process al-stay: no connection attached for 1m0s: it stayed at SIGTERM, and its group was killed",
		"[process.Manager] Process al-stay exited with code -1, terminated by SIGKILL, asked to end by this daemon (unattended)")
	c, lines := rawLineConn(t)
	if _, found, running, _, _, _ := m.Reattach(c, "al-stay", 0); !found || running {
		t.Fatalf("reattach found %v, running %v, want true and false", found, running)
	}
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 5*time.Second))
	const want = `{"type":"stream","processId":"al-stay","stream":"exit","seq":2,"exitCode":-1,"signal":"SIGKILL","killedBy":"unattended","end":{"at":<n>,"len":119}}`
	if exit != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
}

// TestUnheldNoLineAfterAClientKill: a child that a process.kill ended gets no end line
// and no signal of the check, and the check returns at its exit (87375cc7, measured on
// Linux, macOS and Windows).
func TestUnheldNoLineAfterAClientKill(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeUnheldTicker(t)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	p, _ := spawnUnheldHelper(t, m, "al-kill", "term-exit0")
	<-f.steps
	if err := m.Kill("al-kill", "TERM"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the child did not end at the kill")
	}
	f.waitStopped(t)
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM"}) {
		t.Errorf("signals = %q, want only the TERM of the kill", got)
	}
	logs := buf.String()
	if strings.Contains(logs, "Process al-kill: no connection attached") {
		t.Errorf("an end line after a client kill:\n%s", logs)
	}
	if !strings.Contains(logs, "[process.Manager] Process al-kill exited with code 0, asked to end by this daemon (client)\n") {
		t.Errorf("no client exited line:\n%s", logs)
	}
}

// TestUnheldNoTermToAChildThatLeft: a child that exited by itself and is not reaped
// yet gets no SIGTERM of the end, also while the gate is still open (claustrum's own).
// The child is the test's own, in a group of its own.
func TestUnheldNoTermToAChildThatLeft(t *testing.T) {
	if !exitSeenBeforeReap {
		t.Skip("no exit is seen before the reap on this system")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = BuildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "echo"})
	cmd.SysProcAttr = sysproc.NewSysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	for deadline := time.Now().Add(10 * time.Second); sysproc.SurelyRuns(cmd.Process); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the helper did not exit in 10 s")
		}
	}
	p := &Proc{
		id: "al-left2", cmd: cmd, group: &ProcGroup{}, running: true, done: make(chan struct{}),
		unheldTTL: time.Minute, unheldSince: time.Now().Add(-time.Hour), stopsBegan: &atomic.Bool{},
	}
	signals := countSignalsTo(t, p)
	m := newTestProcManager(t)
	if again := m.endUnheld(p, time.Now(), time.Millisecond); again {
		t.Error("endUnheld asks for another step for a child that left")
	}
	if n := signals.Load(); n != 0 {
		t.Errorf("%d signals to a child that left", n)
	}
	p.mu.Lock()
	termed, by := p.unheldTermed, p.killedBy
	p.mu.Unlock()
	if termed || by != "" {
		t.Errorf("termed %v, killedBy %q for a child that left", termed, by)
	}
}

// TestUnheldNoTermAtAStepAfterAClientKill: a check step that comes after a child that a
// process.kill ended has exited, before its reap, sends nothing and logs no end line
// (claustrum's own: the end tests that the child still runs). The seam sends the step
// while the exit goroutine waits, and it returns only once the check returned from that
// step. So the gate stays open for the whole step.
func TestUnheldNoTermAtAStepAfterAClientKill(t *testing.T) {
	if !exitSeenBeforeReap {
		t.Skip("no exit is seen before the reap on this system")
	}
	old := unheldEndGrace
	unheldEndGrace = 100 * time.Millisecond
	t.Cleanup(func() { unheldEndGrace = old })
	buf := captureLogBuf(t)
	f := fakeUnheldTicker(t)
	sent := recordSignalsByProc(t)
	took := make(chan bool, 1)
	oldSeen := unheldExitSeen
	unheldExitSeen = func(p *Proc) {
		select {
		case f.ticks <- unheldSinceOf(p).Add(time.Hour):
		case <-time.After(30 * time.Second):
			took <- false
			return
		}
		// The gate stays open until the check returned from the step.
		select {
		case <-f.stopped:
			took <- true
		case <-time.After(30 * time.Second):
			took <- false
		}
	}
	t.Cleanup(func() { unheldExitSeen = oldSeen })
	m := newTestProcManager(t)
	p, _ := spawnUnheldHelper(t, m, "al-kst", "term-exit0")
	<-f.steps
	if err := m.Kill("al-kst", "TERM"); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-took:
		if !ok {
			t.Fatal("the check took no step at the exit, or did not return from it, in 30 s")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the exit of the child was not seen in 30 s")
	}
	f.waitStopped(t)
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the child did not end at the kill")
	}
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM"}) {
		t.Errorf("signals = %q, want only the TERM of the kill", got)
	}
	if strings.Contains(buf.String(), "Process al-kst: no connection attached") {
		t.Errorf("an end line at a step after a client kill:\n%s", buf.String())
	}
}

// refuseUnheldTerm makes each SIGTERM fail with err, with nothing sent, while the
// returned switch is on. Every other signal goes on to its target.
func refuseUnheldTerm(t *testing.T, err error) *atomic.Bool {
	t.Helper()
	refusing := &atomic.Bool{}
	refusing.Store(true)
	inner := signalGroup
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		if signame == "TERM" && refusing.Load() {
			return &SignalFailed{Name: "terminated", Err: err}
		}
		return inner(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = inner })
	return refusing
}

const refusedLineEnd = ": no connection attached for 1m0s, but it could not be sent SIGTERM\n"

// TestUnheldRefusedTermTriesAgain: a SIGTERM refused with EPERM logs one line, sends no
// SIGKILL, and goes again at each later step (87375cc7, measured on Linux and macOS).
func TestUnheldRefusedTermTriesAgain(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeUnheldTicker(t)
	refuseUnheldTerm(t, syscall.EPERM)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	p, _ := spawnUnheldHelper(t, m, "rf-again", "ignore-term")
	<-f.steps
	at := unheldSinceOf(p).Add(p.unheldTTL)
	for i := range 3 {
		f.settle(t, at.Add(time.Duration(i)*15*time.Second))
	}
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM", "TERM", "TERM"}) {
		t.Errorf("signals = %q, want three refused TERM and no KILL", got)
	}
	if n := strings.Count(buf.String(), "Process rf-again"+refusedLineEnd); n != 1 {
		t.Errorf("%d refused lines, want 1:\n%s", n, buf.String())
	}
	if by := p.KilledByForTest(); by != "" {
		t.Errorf("killedBy = %q after refused signals, want none", by)
	}
}

// TestUnheldRefusalFromTheSignalCall: an EPERM of the call that signals the child counts
// as a refusal through the real ProcGroup.Signal. The seam takes only the SIGTERM.
func TestUnheldRefusalFromTheSignalCall(t *testing.T) {
	buf := captureLogBuf(t)
	fakeUnheldTicker(t)
	realOne := sysproc.SignalOne
	sysproc.SignalOne = func(proc *os.Process, sig syscall.Signal) error {
		if sig == syscall.SIGTERM {
			return syscall.EPERM
		}
		return realOne(proc, sig)
	}
	t.Cleanup(func() { sysproc.SignalOne = realOne })
	m := newTestProcManager(t)
	p, _ := spawnUnheldHelper(t, m, "rf-call", "ignore-term")
	if !m.endUnheld(p, unheldSinceOf(p).Add(p.unheldTTL), time.Millisecond) {
		t.Error("no further step after an EPERM of the signal call")
	}
	if n := strings.Count(buf.String(), "Process rf-call"+refusedLineEnd); n != 1 {
		t.Errorf("%d refused lines, want 1:\n%s", n, buf.String())
	}
}

// TestUnheldRefusedTermAcrossAReattach: no SIGTERM goes while a connection is attached.
// After a new detach the next one waits for the whole time, and no second line comes
// (87375cc7, measured on Linux and macOS).
func TestUnheldRefusedTermAcrossAReattach(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeUnheldTicker(t)
	refuseUnheldTerm(t, syscall.EPERM)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	p, _ := spawnUnheldHelper(t, m, "rf-back", "ignore-term")
	<-f.steps
	f.settle(t, unheldSinceOf(p).Add(p.unheldTTL))
	c, _ := pipeConn(t)
	if _, found, _, _, _, _ := m.Reattach(c, "rf-back", 0); !found {
		t.Fatal("reattach found nothing")
	}
	f.settle(t, time.Now().Add(time.Hour))
	if got := sent(p.cmd.Process); len(got) != 1 {
		t.Fatalf("signals = %q with a connection attached, want only the first TERM", got)
	}
	m.DetachConn(c)
	since := unheldSinceOf(p)
	f.settle(t, since.Add(p.unheldTTL-time.Millisecond))
	if got := sent(p.cmd.Process); len(got) != 1 {
		t.Fatalf("signals = %q before the whole time, want only the first TERM", got)
	}
	f.settle(t, since.Add(p.unheldTTL))
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM", "TERM"}) {
		t.Errorf("signals = %q, want a second TERM after the whole time", got)
	}
	if n := strings.Count(buf.String(), "Process rf-back"+refusedLineEnd); n != 1 {
		t.Errorf("%d refused lines, want 1:\n%s", n, buf.String())
	}
}

// TestUnheldRefusedThenSignalable: the first step after the refusal ends runs the usual
// end, with its signals and lines. 87375cc7 does this for a child that stays on Linux,
// and for one that leaves on macOS (measured). The wait is shrunk here.
func TestUnheldRefusedThenSignalable(t *testing.T) {
	old := unheldEndGrace
	unheldEndGrace = 200 * time.Millisecond
	t.Cleanup(func() { unheldEndGrace = old })
	buf := captureLogBuf(t)
	f := fakeUnheldTicker(t)
	refusing := refuseUnheldTerm(t, syscall.EPERM)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	p, _ := spawnUnheldHelper(t, m, "rf-open", "ignore-term")
	<-f.steps
	at := unheldSinceOf(p).Add(p.unheldTTL)
	f.settle(t, at)
	refusing.Store(false)
	f.step(t, at.Add(15*time.Second))
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the child did not end")
	}
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM", "TERM", "KILL"}) {
		t.Errorf("signals = %q, want the refused TERM, a TERM and the group KILL", got)
	}
	wantLinesInOrder(t, buf.String(),
		"[process.Manager] Process rf-open: no connection attached for 1m0s, but it could not be sent SIGTERM",
		"[process.Manager] Process rf-open: no connection attached for 1m0s: it stayed at SIGTERM, and its group was killed",
		"[process.Manager] Process rf-open exited with code -1, terminated by SIGKILL, asked to end by this daemon (unattended)")
}

// TestUnheldRefusedThenOwnExit: a refused child that exits by itself ends the checks,
// with its plain exited line and no signal after the refusal (87375cc7, measured on
// Linux and macOS). The child leaves when its stdin closes.
func TestUnheldRefusedThenOwnExit(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeUnheldTicker(t)
	refuseUnheldTerm(t, syscall.EPERM)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	exe, env := helperCommand(t, "cat")
	p, err := m.SpawnWithTempRoot(c, "rf-own", exe, nil, "", env, false, nil, 0, TempRootAsk{}, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { endOwnChild(t, p) })
	<-f.steps
	m.DetachConn(c)
	f.settle(t, unheldSinceOf(p).Add(p.unheldTTL))
	_ = p.stdin.Close()
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the child did not exit")
	}
	f.waitStopped(t)
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM"}) {
		t.Errorf("signals = %q, want only the refused TERM", got)
	}
	logs := buf.String()
	if !strings.Contains(logs, "[process.Manager] Process rf-own exited with code 0\n") {
		t.Errorf("no plain exited line:\n%s", logs)
	}
	if strings.Contains(logs, "Process rf-own: no connection attached for 1m0s: it") {
		t.Errorf("an end line after an own exit:\n%s", logs)
	}
}

// TestUnheldOtherTermErrorStops: a SIGTERM that fails with EIO, an error other than EPERM or ESRCH,
// stops the end, with no line and no SIGKILL (claustrum's own, not measured).
func TestUnheldOtherTermErrorStops(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeUnheldTicker(t)
	refuseUnheldTerm(t, syscall.EIO)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	p, _ := spawnUnheldHelper(t, m, "rf-eio", "ignore-term")
	<-f.steps
	f.step(t, unheldSinceOf(p).Add(p.unheldTTL))
	f.waitStopped(t)
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM"}) {
		t.Errorf("signals = %q, want one TERM", got)
	}
	if strings.Contains(buf.String(), "Process rf-eio"+refusedLineEnd) {
		t.Errorf("a refused line for another error:\n%s", buf.String())
	}
}

// TestUnheldRefusedLineForEachChild: each refused child logs its own line (87375cc7,
// measured on Linux). No macOS row has two refused children.
func TestUnheldRefusedLineForEachChild(t *testing.T) {
	buf := captureLogBuf(t)
	fakeUnheldTicker(t)
	refuseUnheldTerm(t, syscall.EPERM)
	m := newTestProcManager(t)
	for _, id := range []string{"rf-one", "rf-two"} {
		p, _ := spawnUnheldHelper(t, m, id, "ignore-term")
		if !m.endUnheld(p, unheldSinceOf(p).Add(p.unheldTTL), time.Millisecond) {
			t.Errorf("%s: no further step after a refusal", id)
		}
		if n := strings.Count(buf.String(), "Process "+id+refusedLineEnd); n != 1 {
			t.Errorf("%s: %d refused lines, want 1", id, n)
		}
	}
}

// TestUnheldRefusedStopsAtTheShutdown: once the shutdown began, a refused child gets no
// further SIGTERM of the end, and the check returns (claustrum's own, not measured).
func TestUnheldRefusedStopsAtTheShutdown(t *testing.T) {
	f := fakeUnheldTicker(t)
	refuseUnheldTerm(t, syscall.EPERM)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	p, _ := spawnUnheldHelper(t, m, "rf-stop", "ignore-term")
	<-f.steps
	at := unheldSinceOf(p).Add(p.unheldTTL)
	f.settle(t, at)
	m.RefuseStarts()
	f.step(t, at.Add(15*time.Second))
	f.waitStopped(t)
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM"}) {
		t.Errorf("signals = %q, want only the TERM before the shutdown", got)
	}
}
