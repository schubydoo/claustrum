//go:build unix

package supervisor

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// spawnHelperTempRoot starts this test binary in the helper mode with the id tr, as a
// spawn with the temp-folder members ask. A fake step gives no folder and keeps what
// the spawn binds. It returns the process and a call that gives the bound end, and
// whether the spawn bound at all.
func spawnHelperTempRoot(t *testing.T, m *Manager, c *conn.Conn, ask TempRootAsk, mode string, args ...string) (*Proc, func() (func(), bool)) {
	t.Helper()
	return spawnHelperTempRootID(t, m, c, "tr", ask, mode, args...)
}

// spawnHelperTempRootID is spawnHelperTempRoot with the id id.
func spawnHelperTempRootID(t *testing.T, m *Manager, c *conn.Conn, id string, ask TempRootAsk, mode string, args ...string) (*Proc, func() (func(), bool)) {
	t.Helper()
	var mu sync.Mutex
	var end func()
	bound := false
	old := privateTempRoot
	privateTempRoot = func(env []string, runDir string) ([]string, *rpc.CLITempRoot, func(), func(func())) {
		return env, nil, func() {}, func(e func()) {
			mu.Lock()
			end, bound = e, true
			mu.Unlock()
		}
	}
	t.Cleanup(func() { privateTempRoot = old })
	exe, env := helperCommand(t, mode)
	p, err := m.spawnWith(c, id, exe, args, "", env, false, nil, 0, ask, nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	return p, func() (func(), bool) {
		mu.Lock()
		defer mu.Unlock()
		return end, bound
	}
}

// setTempRootEndGraceForTest shortens the wait after the SIGTERM until the test ends.
func setTempRootEndGraceForTest(t *testing.T, d time.Duration) {
	t.Helper()
	old := tempRootEndGrace
	tempRootEndGrace = d
	t.Cleanup(func() { tempRootEndGrace = old })
}

// TestLostTempRootEndsAChildThatRunsOn: the end of a lost temp folder sends SIGTERM,
// and SIGKILL after the wait to a child that ignores it. The exit frame and the
// exited line carry the killedBy "temp_root", and the ended line comes first
// (4c111e86, measured on Linux and macOS, the SIGKILL and the line order on macOS).
// No second SIGKILL follows at the exit. The wait is shortened.
func TestLostTempRootEndsAChildThatRunsOn(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setTempRootEndGraceForTest(t, 300*time.Millisecond)
	sent := recordSignalsByProc(t)
	c, lines := rawLineConn(t)
	p, bound := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "ignore-term")
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	end, ok := bound()
	if !ok || end == nil {
		t.Fatal("the spawn bound no end, want one for cliTempRootEnd")
	}
	end()
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 15*time.Second))
	const want = `{"type":"stream","processId":"tr","stream":"exit","seq":2,"exitCode":-1,"signal":"SIGKILL","killedBy":"temp_root","end":{"at":<n>,"len":113}}`
	if exit != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM", "KILL"}) {
		t.Errorf("signals to the child = %q, want TERM then KILL", got)
	}
	wantLinesInOrder(t, logs.String(),
		"[process.Manager] Process tr is being ended: its private temp folder is lost",
		"[process.Manager] Process tr exited with code -1, terminated by SIGKILL, asked to end by this daemon (temp_root)")
}

// TestLostTempRootKeepsItsKilledByAfterAClientKill: a process.kill in the wait sends
// its SIGTERM, the end still sends the SIGKILL, and the exit frame keeps the killedBy
// "temp_root" (4c111e86, measured on macOS).
func TestLostTempRootKeepsItsKilledByAfterAClientKill(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setTempRootEndGraceForTest(t, 500*time.Millisecond)
	sent := recordSignalsByProc(t)
	c, lines := rawLineConn(t)
	p, bound := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "ignore-term")
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	end, _ := bound()
	end()
	for deadline := time.Now().Add(5 * time.Second); len(sent(p.cmd.Process)) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no SIGTERM from the end")
		}
	}
	p.signalIfLive("TERM", "client")
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 15*time.Second))
	const want = `{"type":"stream","processId":"tr","stream":"exit","seq":2,"exitCode":-1,"signal":"SIGKILL","killedBy":"temp_root","end":{"at":<n>,"len":113}}`
	if exit != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM", "TERM", "KILL"}) {
		t.Errorf("signals to the child = %q, want TERM, the client TERM, then KILL", got)
	}
}

// TestLostTempRootTakesTheKilledByAfterAClientKill: a child that ignores the SIGTERM
// of a process.kill and then loses its folder exits with the killedBy "temp_root"
// after the SIGKILL of the end (4c111e86, measured on macOS, 8 of 8).
func TestLostTempRootTakesTheKilledByAfterAClientKill(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setTempRootEndGraceForTest(t, 300*time.Millisecond)
	sent := recordSignalsByProc(t)
	c, lines := rawLineConn(t)
	p, bound := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "ignore-term")
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	if !p.signalIfLive("TERM", "client") {
		t.Fatal("the client SIGTERM found no live child")
	}
	end, _ := bound()
	end()
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 15*time.Second))
	const want = `{"type":"stream","processId":"tr","stream":"exit","seq":2,"exitCode":-1,"signal":"SIGKILL","killedBy":"temp_root","end":{"at":<n>,"len":113}}`
	if exit != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM", "TERM", "KILL"}) {
		t.Errorf("signals to the child = %q, want the client TERM, the TERM of the end, then KILL", got)
	}
	wantLinesInOrder(t, logs.String(),
		"[process.Manager] Process tr exited with code -1, terminated by SIGKILL, asked to end by this daemon (temp_root)")
}

// TestTempRootReplacesOnlyClient: a temp_root signal keeps an earlier "shutdown" or
// "limit" (claustrum's own, not measured).
func TestTempRootReplacesOnlyClient(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	recordSignalsByProc(t)
	c, lines := rawLineConn(t)
	for i, first := range []string{"shutdown", "limit"} {
		p, _ := spawnHelperTempRootID(t, m, c, "tr"+first, TempRootAsk{Folder: true, End: true}, "ignore-term")
		nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
		p.mu.Lock()
		p.killedBy = first
		live, err := p.signalIfLiveLocked("TERM", "temp_root")
		got := p.killedBy
		p.mu.Unlock()
		if !live || err != nil || got != first {
			t.Errorf("row %d: live %v, err %v, killedBy %q, want %q", i, live, err, got, first)
		}
	}
}

// TestTempRootKeepsClientAfterTheExit: a child that ended on the SIGTERM of a
// process.kill keeps the killedBy "client", also when the temp_root signal comes after
// its exit and before the exit goroutine marks it reaped (4c111e86, measured on macOS:
// kill then loss gives "client"). The test holds p.mu from the client SIGTERM on, so
// the reap flag waits.
func TestTempRootKeepsClientAfterTheExit(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	p, _ := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "term-exit0")
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	// p.mu is held from before the client SIGTERM, so the exit goroutine cannot set
	// p.reaped before the temp_root signal.
	p.mu.Lock()
	if live, _ := p.signalIfLiveLocked("TERM", "client"); !live {
		p.mu.Unlock()
		t.Fatal("the client SIGTERM found no live child")
	}
	for deadline := time.Now().Add(10 * time.Second); sysproc.SurelyRuns(p.cmd.Process); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			p.mu.Unlock()
			t.Fatal("the child did not exit on the client SIGTERM")
		}
	}
	live, _ := p.signalIfLiveLocked("TERM", "temp_root")
	got := p.killedBy
	p.mu.Unlock()
	if !live || got != "client" {
		t.Errorf("after the exit: live %v, killedBy %q, want the window open and %q", live, got, "client")
	}
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 15*time.Second))
	if !strings.Contains(exit, `"killedBy":"client"`) {
		t.Errorf("exit frame = %s, want the killedBy client", exit)
	}
}

// TestLostTempRootEndNeedsTheAsk: a spawn with no cliTempRootEnd binds nil, so a loss
// of its folder sends it nothing (4c111e86, measured on macOS). A spawn with no
// cliTempRoot binds nothing.
func TestLostTempRootEndNeedsTheAsk(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, lines := rawLineConn(t)
	_, bound := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true}, "ignore-term")
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	if end, ok := bound(); !ok || end != nil {
		t.Errorf("bound %v, end set %v: want a bind with no end", ok, end != nil)
	}
}

// TestLostTempRootStartsNoEndAtShutdown: once the shutdown began, the end sends no
// signal and logs no line (claustrum's own, not measured).
func TestLostTempRootStartsNoEndAtShutdown(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	sent := recordSignalsByProc(t)
	c, lines := rawLineConn(t)
	p, bound := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "ignore-term")
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	m.RefuseStarts()
	m.endForLostTempRoot(p, time.Millisecond)
	if got := sent(p.cmd.Process); len(got) != 0 {
		t.Errorf("signals after the shutdown began = %q, want none", got)
	}
	if got := logs.String(); strings.Contains(got, "is being ended") {
		t.Errorf("log %q, want no ended line", got)
	}
	if end, _ := bound(); end == nil {
		t.Error("the spawn bound no end")
	}
}

// TestShutdownDuringLostTempRootEndLogsNoExitLine: the shutdown sweep ends a child
// whose temp_root end still waits, and no exited line comes (4c111e86, measured on
// Linux). The ended line stays, with the cut id (4c111e86, measured on Linux and
// macOS).
func TestShutdownDuringLostTempRootEndLogsNoExitLine(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setTempRootEndGraceForTest(t, 20*time.Second)
	sent := recordSignalsByProc(t)
	c, lines := rawLineConn(t)
	p, bound := spawnHelperTempRootID(t, m, c, "tr-long-id", TempRootAsk{Folder: true, End: true}, "ignore-term")
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	end, _ := bound()
	end()
	for deadline := time.Now().Add(5 * time.Second); len(sent(p.cmd.Process)) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no SIGTERM from the end")
		}
	}
	if n := m.KillAllCount(); n != 1 {
		t.Fatalf("the sweep ended %d children, want 1", n)
	}
	nextLine(t, lines, `"stream":"exit"`, 15*time.Second)
	got := logs.String()
	if !strings.Contains(got, "[process.Manager] Process tr-long-… is being ended: its private temp folder is lost\n") {
		t.Errorf("log %q, want the ended line", got)
	}
	if strings.Contains(got, "Process tr-long-… exited") {
		t.Errorf("log %q, want no exited line", got)
	}
}

// TestTempRootEndGraceValue pins the wait after the SIGTERM at 3 s (4c111e86,
// measured on macOS).
func TestTempRootEndGraceValue(t *testing.T) {
	if tempRootEndGrace != 3*time.Second {
		t.Errorf("wait %v, want 3s", tempRootEndGrace)
	}
}
