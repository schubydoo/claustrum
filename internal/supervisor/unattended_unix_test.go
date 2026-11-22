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
