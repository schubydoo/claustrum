//go:build unix

package supervisor

import (
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
)

// TestKillReturnsARefusedGroupKillOfAnExitedChild: a client KILL whose group kill the
// system refuses returns the *SignalFailed in the exit drain of a plain spawn and in the
// hold of an exited launcher. 87375cc7 answers the error in the drain (measured on Linux and
// macOS, EPERM) and in the hold (measured on Linux, EPERM). The hold gives the claim back. A kill that a taken claim stops returns nil. 87375cc7
// answers success after the end of a launcher (measured on Linux). In the drain that is claustrum's own.
func TestKillReturnsARefusedGroupKillOfAnExitedChild(t *testing.T) {
	var sent atomic.Int32
	old := signalGroup
	signalGroup = func(*ProcGroup, *os.Process, string) error {
		sent.Add(1)
		return &SignalFailed{Name: "killed", Err: syscall.EPERM}
	}
	t.Cleanup(func() { signalGroup = old })
	var probes atomic.Int32
	oldInUse := SetPidInUseForTest(func(int) bool { probes.Add(1); return false })
	t.Cleanup(func() { SetPidInUseForTest(oldInUse) })

	for _, tc := range []struct {
		name       string
		held       bool
		claimTaken bool
		wantErr    bool
		wantProbes int32
	}{
		{"drain", false, false, true, 1},
		{"hold", true, false, true, 0},
		{"drain after its kill", false, true, false, 1},
		{"hold after the end", true, true, false, 0},
	} {
		sent.Store(0)
		probes.Store(0)
		m := newTestProcManager(t)
		p := m.AddExitedChildForTest("x", noSuchPid, tc.held)
		if tc.claimTaken {
			p.groupKillClaimed.Store(true)
		}
		err := m.Kill("x", "KILL")
		var failed *SignalFailed
		if got := errors.As(err, &failed); got != tc.wantErr {
			t.Errorf("%s: kill error %v, want a *SignalFailed %v", tc.name, err, tc.wantErr)
		}
		if failed != nil && failed.Error() != "operation not permitted" {
			t.Errorf("%s: error text %q, want %q", tc.name, failed.Error(), "operation not permitted")
		}
		wantSent := int32(0)
		if tc.wantErr {
			wantSent = 1
		}
		if n := sent.Load(); n != wantSent {
			t.Errorf("%s: group kills = %d, want %d", tc.name, n, wantSent)
		}
		if n := probes.Load(); n != tc.wantProbes {
			t.Errorf("%s: pid probes = %d, want %d", tc.name, n, tc.wantProbes)
		}
		if tc.held && !tc.claimTaken && (p.groupKillClaimed.Load() || p.heldGroupKilled) {
			t.Errorf("%s: the refused kill kept the claim or set the mark for the count", tc.name)
		}
	}
}
