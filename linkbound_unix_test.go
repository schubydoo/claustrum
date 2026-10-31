//go:build unix

package main

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordSignals wraps the signalGroup seam and records each signal name sent to
// the process with the given pid, in order. The real signal still goes out, to the
// test's own child only.
func recordSignals(t *testing.T, pid *atomic.Int64) func() []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	real := signalGroup
	signalGroup = func(g *procGroup, proc *os.Process, signame string) error {
		mu.Lock()
		if int64(proc.Pid) == pid.Load() {
			got = append(got, signame)
		}
		mu.Unlock()
		return real(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = real })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// TestLinkBoundUnattendedEndHUP: a child that takes the SIGHUP ends by it, and the
// exit line is the one of 683cc6a9 (measured on Linux).
// Under a runner that ignores SIGHUP (nohup), the helper ignores it too. The
// child then dies by the SIGKILL, and this test fails.
func TestLinkBoundUnattendedEndHUP(t *testing.T) {
	buf := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	exe, env := helperCommand(t, "sleep")
	p, err := m.spawnVia(&conn{closed: true}, "u1", exe, []string{"60"}, "", env, false, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the unattended process did not end")
	}
	logs := buf.String()
	end := strings.Index(logs, "[process.Manager] Process u1: no connection attached for 1s; ending it")
	exit := strings.Index(logs, "[process.Manager] Process u1 exited with code -1, terminated by SIGHUP, asked to end by this daemon (unattended)")
	if end < 0 || exit < end {
		t.Errorf("want the ending line, then the SIGHUP exit line, in:\n%s", logs)
	}
}

// TestLinkBoundUnattendedEndKillsAfterGrace: a child that ignores SIGHUP gets the
// SIGKILL after the grace, and the exit line names SIGKILL. 683cc6a9 logs it
// about 7 s after the ending line (measured on Linux). The grace is shrunk here.
func TestLinkBoundUnattendedEndKillsAfterGrace(t *testing.T) {
	old := unattendedEndGrace
	unattendedEndGrace = 300 * time.Millisecond
	t.Cleanup(func() { unattendedEndGrace = old })
	buf := captureLogBuf(t)
	var pid atomic.Int64
	pid.Store(-1)
	signals := recordSignals(t, &pid)
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	exe, env := helperCommand(t, "ignore-hup")
	c, _ := pipeConn(t)
	p, err := m.spawnVia(c, "u2", exe, nil, "", env, false, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pid.Store(int64(p.pid))
	// The child ignores SIGHUP once it printed ready. Only then does the link go.
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.mu.Lock()
		n := len(p.buffer)
		p.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper never printed ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.detachConn(c)
	gone := time.Now()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the unattended process did not end")
	}
	if took := time.Since(gone); took < time.Second+unattendedEndGrace {
		t.Errorf("the process ended %s after the link went, want at least the time plus the grace", took)
	}
	if got := strings.Join(signals(), ","); got != "HUP,KILL" {
		t.Errorf("signals = %s, want HUP,KILL", got)
	}
	logs := buf.String()
	end := strings.Index(logs, "[process.Manager] Process u2: no connection attached for 1s; ending it")
	exit := strings.Index(logs, "[process.Manager] Process u2 exited with code -1, terminated by SIGKILL, asked to end by this daemon (unattended)")
	if end < 0 || exit < end {
		t.Errorf("want the ending line, then the SIGKILL exit line, in:\n%s", logs)
	}
}
