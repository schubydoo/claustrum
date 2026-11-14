//go:build linux || darwin

package supervisor

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestLostTempRootKillsTheGroupOfAChildThatEnds: a child that ends on the SIGTERM of
// the end gets a group SIGKILL before its reap, and a process in its group ends with
// it. The exit frame carries the killedBy "temp_root" (claustrum's rule,
// decision-099). The grandchild is the test's own.
func TestLostTempRootKillsTheGroupOfAChildThatEnds(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setTempRootEndGraceForTest(t, 10*time.Second)
	sent := recordSignalsByProc(t)
	pidFile := filepath.Join(t.TempDir(), "kid")
	c, lines := rawLineConn(t)
	p, bound := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "term-exit0-kid", pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	kid := waitPIDFile(t, pidFile)
	gone := false
	t.Cleanup(func() {
		if !gone {
			_ = syscall.Kill(kid, syscall.SIGKILL)
		}
	})
	end, _ := bound()
	end()
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 15*time.Second))
	const want = `{"type":"stream","processId":"tr","stream":"exit","seq":2,"exitCode":0,"killedBy":"temp_root","end":{"at":<n>,"len":93}}`
	if exit != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM", "KILL"}) {
		t.Errorf("signals = %q, want TERM, then the group KILL", got)
	}
	for deadline := time.Now().Add(5 * time.Second); !gone; time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(kid, 0) != nil {
			gone = true
		} else if time.Now().After(deadline) {
			t.Fatalf("the process %d in the group lives on", kid)
		}
	}
}

// TestNoTempRootGroupKillAfterAClientEnd: a cliTempRootEnd child that a process.kill
// ends, with no loss of its folder, gets no group SIGKILL at its exit (claustrum's own).
func TestNoTempRootGroupKillAfterAClientEnd(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	sent := recordSignalsByProc(t)
	c, lines := rawLineConn(t)
	p, _ := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "term-exit0")
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	p.signalIfLive("TERM", "client")
	nextLine(t, lines, `"stream":"exit"`, 15*time.Second)
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM"}) {
		t.Errorf("signals = %q, want the TERM alone", got)
	}
}

// TestLostTempRootKillsTheGroupOfAChildThatEndsAtTheKill: a child that ends after the
// wait of the end, before its SIGKILL, still has its group killed once. The stub sends
// no SIGTERM, so the child runs to the wait. The seam then kills the child alone and
// waits for its reap. The grandchild is the test's own (claustrum's own rule).
func TestLostTempRootKillsTheGroupOfAChildThatEndsAtTheKill(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setTempRootEndGraceForTest(t, 200*time.Millisecond)
	realSignal := signalGroup
	var mu sync.Mutex
	var sent []string
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		mu.Lock()
		sent = append(sent, signame)
		mu.Unlock()
		if signame == "TERM" {
			return nil
		}
		return realSignal(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = realSignal })
	oldSeam := tempRootBeforeKill
	tempRootBeforeKill = func(p *Proc) {
		_ = syscall.Kill(p.cmd.Process.Pid, syscall.SIGKILL)
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			p.mu.Lock()
			reaped := p.reaped
			p.mu.Unlock()
			if reaped || time.Now().After(deadline) {
				return
			}
		}
	}
	t.Cleanup(func() { tempRootBeforeKill = oldSeam })
	pidFile := filepath.Join(t.TempDir(), "kid")
	c, lines := rawLineConn(t)
	_, bound := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "term-exit0-kid", pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	kid := waitPIDFile(t, pidFile)
	gone := false
	t.Cleanup(func() {
		if !gone {
			_ = syscall.Kill(kid, syscall.SIGKILL)
		}
	})
	end, _ := bound()
	end()
	nextLine(t, lines, `"stream":"exit"`, 15*time.Second)
	for deadline := time.Now().Add(5 * time.Second); !gone; time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(kid, 0) != nil {
			gone = true
		} else if time.Now().After(deadline) {
			t.Fatalf("the process %d in the group lives on", kid)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(sent, []string{"TERM", "KILL"}) {
		t.Errorf("signals = %q, want TERM, then one group KILL", sent)
	}
}

// TestLostTempRootKillsTheGroupAfterAClientKill: a process.kill that the child
// survives keeps the killedBy "client". The SIGTERM of the end then ends the child,
// and its group still gets one SIGKILL before the reap. The stub drops the first
// SIGTERM. The grandchild is the test's own (claustrum's own rule).
func TestLostTempRootKillsTheGroupAfterAClientKill(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setTempRootEndGraceForTest(t, 10*time.Second)
	realSignal := signalGroup
	var mu sync.Mutex
	var sent []string
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		mu.Lock()
		sent = append(sent, signame)
		first := len(sent) == 1
		mu.Unlock()
		if first {
			return nil
		}
		return realSignal(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = realSignal })
	pidFile := filepath.Join(t.TempDir(), "kid")
	c, lines := rawLineConn(t)
	p, bound := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "term-exit0-kid", pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	kid := waitPIDFile(t, pidFile)
	gone := false
	t.Cleanup(func() {
		if !gone {
			_ = syscall.Kill(kid, syscall.SIGKILL)
		}
	})
	p.signalIfLive("TERM", "client")
	end, _ := bound()
	end()
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 15*time.Second))
	const want = `{"type":"stream","processId":"tr","stream":"exit","seq":2,"exitCode":0,"killedBy":"client","end":{"at":<n>,"len":90}}`
	if exit != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	for deadline := time.Now().Add(5 * time.Second); !gone; time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(kid, 0) != nil {
			gone = true
		} else if time.Now().After(deadline) {
			t.Fatalf("the process %d in the group lives on", kid)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(sent, []string{"TERM", "TERM", "KILL"}) {
		t.Errorf("signals = %q, want the client TERM, the TERM of the end, then one group KILL", sent)
	}
}

// TestNoTempRootGroupKillAfterTheChildLeftItsGroup: a child that left the group of its
// spawn gets no group SIGKILL at its exit after the SIGTERM of the end (claustrum's
// own rule, decision-099). The seam sends the SIGTERM to the child alone and delivers
// no other signal.
func TestNoTempRootGroupKillAfterTheChildLeftItsGroup(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setTempRootEndGraceForTest(t, 10*time.Second)
	realSignal := signalGroup
	var mu sync.Mutex
	var sent []string
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		mu.Lock()
		sent = append(sent, signame)
		mu.Unlock()
		if signame == "TERM" {
			_ = proc.Signal(syscall.SIGTERM)
		}
		return nil
	}
	t.Cleanup(func() { signalGroup = realSignal })
	c, lines := rawLineConn(t)
	p, bound := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "leave-group-term-exit0")
	t.Cleanup(func() { _ = p.cmd.Process.Kill() })
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	if groupMadeAtSpawn(p.cmd.Process.Pid) {
		t.Fatal("the child is still in the group of its spawn")
	}
	end, _ := bound()
	end()
	nextLine(t, lines, `"stream":"exit"`, 15*time.Second)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(sent, []string{"TERM"}) {
		t.Errorf("signals = %q, want the TERM alone", sent)
	}
}
