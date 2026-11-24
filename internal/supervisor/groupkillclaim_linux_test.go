package supervisor

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"sync/atomic"
	"syscall"
	"testing"
)

// TestKeepChildrenShutdownInTheLastGapSendsNoKill: with -keep-children, a shutdown that
// began before the last test of the end gets no SIGKILL. With no -keep-children the same
// order still sends the one SIGKILL.
func TestKeepChildrenShutdownInTheLastGapSendsNoKill(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep bool
		want []syscall.Signal
	}{
		{"keep", true, nil},
		{"no keep", false, []syscall.Signal{syscall.SIGKILL}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := fakeLeftGroup(t, nil)
			began := &atomic.Bool{}
			began.Store(true)
			p := &Proc{endsGroup: true, keepsGroup: tc.keep, stopsBegan: began}
			p.sendHeldEndKill(fakeGroupNumber)
			if got := sent(); !slices.Equal(got, tc.want) {
				t.Errorf("signals = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRefusedEndKillIsNoCountInTheHold: after a refused SIGKILL of the end, a shutdown in
// the hold sends nothing more and counts no killed group.
func TestRefusedEndKillIsNoCountInTheHold(t *testing.T) {
	real := signalLeftGroup
	var sent atomic.Int32
	signalLeftGroup = func(int, syscall.Signal) error { sent.Add(1); return errors.New("refused") }
	t.Cleanup(func() { signalLeftGroup = real })
	p := &Proc{endsGroup: true, leaderHeld: true, stopsBegan: &atomic.Bool{}, cmd: &exec.Cmd{Process: &os.Process{Pid: fakeGroupNumber}}}
	p.sendHeldEndKill(fakeGroupNumber)
	if p.killHeldGroupAtStop() {
		t.Error("a shutdown after a refused end kill counts the group")
	}
	if n := sent.Load(); n != 1 {
		t.Errorf("group signals = %d, want only the refused one", n)
	}
}

// TestEndMarksOnlyALandedKillForTheCount: the SIGKILL of the end counts for a sweep in
// the rest of the hold only when it landed.
func TestEndMarksOnlyALandedKillForTheCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"landed", nil, true},
		{"refused", errors.New("refused"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			real := signalLeftGroup
			signalLeftGroup = func(int, syscall.Signal) error { return tc.err }
			t.Cleanup(func() { signalLeftGroup = real })
			p := startExitedLeader(t, "lgmark")
			drained := make(chan struct{})
			close(drained)
			p.settleLeftGroupHeld(drained)
			_ = p.cmd.Wait()
			if p.heldGroupKilled != tc.want {
				t.Errorf("heldGroupKilled = %v, want %v", p.heldGroupKilled, tc.want)
			}
		})
	}
}
