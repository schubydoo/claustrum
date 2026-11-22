//go:build unix

package supervisor

import (
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// spawnAloneHelper starts the helper in mode with args, armed with one minute on a
// raw line connection, and returns the process and its lines.
func spawnAloneHelper(t *testing.T, m *Manager, id, mode string, args ...string) (*Proc, <-chan string) {
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

// endAloneNow hands the check of p one step at the end of its time.
func endAloneNow(t *testing.T, f *aloneTicks, p *Proc) {
	t.Helper()
	f.tick(t, aloneSinceOf(p).Add(p.aloneTTL))
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the child did not end")
	}
}

// TestAloneEndOfAChildThatLeaves: a child that ends on the SIGTERM gets the SIGKILL of
// its group after it left, and a process in its group ends with it. The left line
// comes before the exited line (87375cc7, measured on macOS, on Linux not measured).
// No SIGHUP goes out. The grandchild is the test's own.
func TestAloneEndOfAChildThatLeaves(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeAloneTicker(t)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	pidFile := filepath.Join(t.TempDir(), "kid")
	p, _ := spawnAloneHelper(t, m, "al-left", "term-exit0-kid", pidFile)
	<-f.steps
	kid := waitPIDFile(t, pidFile)
	gone := false
	t.Cleanup(func() {
		if !gone {
			_ = syscall.Kill(kid, syscall.SIGKILL)
		}
	})
	endAloneNow(t, f, p)
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

// TestAloneEndOfAChildThatStays: a child that ignores the SIGTERM gets the group SIGKILL
// at the end of the wait. The stayed line comes before the exited line. A reattach
// gets the exit frame with the killedBy "unattended" (87375cc7, measured on macOS, on
// Linux not measured). The wait is shrunk here.
func TestAloneEndOfAChildThatStays(t *testing.T) {
	old := aloneEndGrace
	aloneEndGrace = 200 * time.Millisecond
	t.Cleanup(func() { aloneEndGrace = old })
	buf := captureLogBuf(t)
	f := fakeAloneTicker(t)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	p, _ := spawnAloneHelper(t, m, "al-stay", "ignore-term")
	<-f.steps
	endAloneNow(t, f, p)
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

// TestAloneNoLineAfterAClientKill: a child that a process.kill ended gets no end line
// and no signal of the check (87375cc7, measured on macOS and Windows).
func TestAloneNoLineAfterAClientKill(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeAloneTicker(t)
	sent := recordSignalsByProc(t)
	m := newTestProcManager(t)
	p, _ := spawnAloneHelper(t, m, "al-kill", "term-exit0")
	<-f.steps
	if err := m.Kill("al-kill", "TERM"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the child did not end at the kill")
	}
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM"}) {
		t.Errorf("signals = %q, want the TERM of the kill alone", got)
	}
	logs := buf.String()
	if strings.Contains(logs, "Process al-kill: no connection attached") {
		t.Errorf("an end line after a client kill:\n%s", logs)
	}
	if !strings.Contains(logs, "[process.Manager] Process al-kill exited with code 0, asked to end by this daemon (client)\n") {
		t.Errorf("no client exited line:\n%s", logs)
	}
}
