//go:build linux || darwin

package supervisor

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/childrec"
)

// TestAwaitRecordsRemovedIsBounded pins the bound of the shutdown wait: a child whose
// exit goroutine never reports does not hold the daemon past shutdownRecordWait.
func TestAwaitRecordsRemovedIsBounded(t *testing.T) {
	old := childrec.SetShutdownRecordWaitForTest(50 * time.Millisecond)
	t.Cleanup(func() { childrec.SetShutdownRecordWaitForTest(old) })
	m := newTestProcManager(t)
	m.RunDir = t.TempDir()
	m.procs["stuck"] = &Proc{id: "stuck", recordDone: make(chan struct{})}
	m.procs["bare"] = &Proc{id: "bare"} // no channel: nothing to wait for
	done := make(chan struct{})
	go func() { defer close(done); m.AwaitRecordsRemoved() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the shutdown wait did not end at its bound")
	}
}

// awaitRecordsQuickly runs AwaitRecordsRemoved with a bound of 10 s and fails if it
// takes 3 s or more.
func awaitRecordsQuickly(t *testing.T, m *Manager) {
	t.Helper()
	old := childrec.SetShutdownRecordWaitForTest(10 * time.Second)
	t.Cleanup(func() { childrec.SetShutdownRecordWaitForTest(old) })
	m.RunDir = t.TempDir()
	start := time.Now()
	m.AwaitRecordsRemoved()
	if d := time.Since(start); d >= 3*time.Second {
		t.Errorf("the shutdown wait took %v, want no wait for a child whose SIGKILL was refused", d)
	}
}

// TestShutdownWaitSkipsARefusedSweepKill: a running child whose sweep SIGKILL is refused
// with EPERM holds no wait for its record (87375cc7, measured on Linux). The seam sends
// nothing, and the child is the test's own.
func TestShutdownWaitSkipsARefusedSweepKill(t *testing.T) {
	m := newTestProcManager(t)
	p, _ := spawnPlainWithMember(t, m, "closed", "stay")
	refuseGroupKills(t)
	m.KillAllCount()
	if !p.stopKillRefused.Load() {
		t.Error("the refused sweep SIGKILL left no mark")
	}
	awaitRecordsQuickly(t, m)
}

// TestShutdownWaitSkipsARefusedKillOfTheAskedEnd: a stream-json child stays after its
// SIGTERM, and the group SIGKILL at the end of the wait is refused with EPERM. It holds no
// wait for its record (87375cc7, measured on Linux). The seam sends neither signal.
func TestShutdownWaitSkipsARefusedKillOfTheAskedEnd(t *testing.T) {
	old := shutdownEndGrace
	shutdownEndGrace = 100 * time.Millisecond
	t.Cleanup(func() { shutdownEndGrace = old })
	m := newTestProcManager(t)
	p, _ := spawnPlainWithMember(t, m, "closed", "stay", "stream-json")
	inner := signalGroup
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		switch signame {
		case "TERM":
			return nil
		case "KILL":
			return &SignalFailed{Name: "killed", Err: syscall.EPERM}
		}
		return inner(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = inner })
	m.KillAllCount()
	if !p.stopKillRefused.Load() {
		t.Error("the refused group SIGKILL of the asked end left no mark")
	}
	awaitRecordsQuickly(t, m)
}
