package supervisor

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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

// TestRefusedEndKillLogsItsLine: a refused SIGKILL of the end logs one line with the
// error (87375cc7, measured on Linux). A number that groupNumberRefused refuses makes no
// kill call and logs nothing. The kill call is a fake.
func TestRefusedEndKillLogsItsLine(t *testing.T) {
	buf := captureLogBuf(t)
	real := signalLeftGroup
	signalLeftGroup = func(int, syscall.Signal) error { return syscall.EPERM }
	t.Cleanup(func() { signalLeftGroup = real })
	p := &Proc{endsGroup: true, stopsBegan: &atomic.Bool{}}
	p.sendHeldEndKill(fakeGroupNumber)
	own := &Proc{endsGroup: true, stopsBegan: &atomic.Bool{}}
	own.sendHeldEndKill(os.Getpid())
	const want = "[process.Manager] the process group of pid 1073741824, which has exited, was not sent SIGKILL: operation not permitted\n"
	if got := buf.String(); !strings.Contains(got, want) || strings.Count(got, "was not sent SIGKILL") != 1 {
		t.Errorf("log = %q, want the one line %q", got, want)
	}
}

// TestRefusedKillInTheWaitKeepsTheEndTimes: a kill in the wait after the SIGTERM that is
// refused holds the claim for a moment under p.mu. The SIGKILL of the end still comes at
// the bound (87375cc7, measured on Linux). The kill call is a fake.
func TestRefusedKillInTheWaitKeepsTheEndTimes(t *testing.T) {
	oldTerm, oldBound := groupTermAfter, groupKillBound
	groupTermAfter, groupKillBound = 50*time.Millisecond, 600*time.Millisecond
	t.Cleanup(func() { groupTermAfter, groupKillBound = oldTerm, oldBound })
	real := signalLeftGroup
	var mu sync.Mutex
	var seen []groupSignalSeen
	signalLeftGroup = func(_ int, sig syscall.Signal) error {
		mu.Lock()
		seen = append(seen, groupSignalSeen{sig: sig, at: time.Now()})
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { signalLeftGroup = real })
	sent := func() []groupSignalSeen {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
	p := startExitedLeader(t, "lgwindow")
	returned := make(chan struct{})
	go func() { p.settleLeftGroupHeld(make(chan struct{})); close(returned) }()
	for deadline := time.Now().Add(10 * time.Second); len(sent()) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the end sent no SIGTERM")
		}
	}
	p.mu.Lock()
	if !p.groupKillClaimed.tryTake() {
		p.mu.Unlock()
		t.Fatal("the claim was taken before the refused kill")
	}
	time.Sleep(100 * time.Millisecond)
	p.groupKillClaimed.giveBack()
	p.mu.Unlock()
	<-returned
	_ = p.cmd.Wait()
	got := sent()
	if !slices.Equal(sigsOf(got), []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("signals of the end = %v, want SIGTERM, then SIGKILL", sigsOf(got))
	}
	if d := got[1].at.Sub(got[0].at); d < 400*time.Millisecond {
		t.Errorf("the SIGKILL came %v after the SIGTERM, want it at the bound", d)
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

// TestEndLogsTheTermLineAfterItsKill: the SIGTERM line of the end comes after its
// SIGKILL, and after the refusal line of a refused one. A shutdown in the wait after the
// SIGTERM logs no SIGTERM line (87375cc7, measured on Linux). The kill call is a fake.
func TestEndLogsTheTermLineAfterItsKill(t *testing.T) {
	oldTerm, oldBound := groupTermAfter, groupKillBound
	groupTermAfter, groupKillBound = 30*time.Millisecond, 2*time.Second
	t.Cleanup(func() { groupTermAfter, groupKillBound = oldTerm, oldBound })
	for _, tc := range []struct {
		name     string
		shutdown bool
	}{
		{"refused end kill", false},
		{"shutdown in the wait", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLogBuf(t)
			termed := make(chan struct{})
			real := signalLeftGroup
			signalLeftGroup = func(_ int, sig syscall.Signal) error {
				if sig == syscall.SIGTERM {
					close(termed)
					return nil
				}
				return syscall.EPERM
			}
			t.Cleanup(func() { signalLeftGroup = real })
			id := "lgline"
			if tc.shutdown {
				id = "lglnstop"
			}
			p := startExitedLeader(t, id)
			returned := make(chan struct{})
			go func() { p.settleLeftGroupHeld(make(chan struct{})); close(returned) }()
			select {
			case <-termed:
			case <-time.After(10 * time.Second):
				t.Fatal("the end sent no SIGTERM")
			}
			if tc.shutdown {
				p.stopsBegan.Store(true)
			}
			<-returned
			_ = p.cmd.Wait()
			termLine := "[process.Manager] Process " + id + " " + groupTermLineTail
			refusal := "[process.Manager] the process group of pid " + strconv.Itoa(p.cmd.Process.Pid) + ", which has exited, was not sent SIGKILL: operation not permitted"
			got := buf.String()
			if tc.shutdown {
				if strings.Contains(got, termLine) || strings.Contains(got, refusal) {
					t.Errorf("log = %q, want no SIGTERM line and no refusal line after a shutdown", got)
				}
				return
			}
			wantLinesInOrder(t, got, refusal, termLine)
		})
	}
}
