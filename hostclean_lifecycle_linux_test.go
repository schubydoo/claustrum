//go:build linux

package main

import (
	"bytes"
	"errors"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The host cleaner's construction and its two background loops. Everything below drives the
// real newHostCleaner / startHostCleaner rather than a hostCleaner literal, which is what the
// other tests in this package build.

// hcFakeSelf points /proc/self/exe at exe and gives this process a stat line with startTicks,
// so newHostCleaner can derive its roots and its own identity from the fake tree.
func hcFakeSelf(t *testing.T, root, exe, startTicks string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(root, "self", "exe")); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	if err := os.MkdirAll(filepath.Join(root, strconv.Itoa(self)), 0o755); err != nil {
		t.Fatal(err)
	}
	stat := strconv.Itoa(self) + " (server) R 1 " + strconv.Itoa(self) + " " +
		strings.Repeat("0 ", 16) + startTicks + " x"
	if err := os.WriteFile(filepath.Join(root, strconv.Itoa(self), "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewHostCleaner(t *testing.T) {
	root, _, _ := fakeProc(t)
	hcFakeSelf(t, root, "/opt/claude/srv/abc123/server", "4242")

	c, err := newHostCleaner("/opt/claude/run/c1/rpc.sock")
	if err != nil {
		t.Fatalf("newHostCleaner: %v", err)
	}
	if c.ownSocket != "/opt/claude/run/c1/rpc.sock" {
		t.Errorf("ownSocket = %q", c.ownSocket)
	}
	if c.ownRunDir != "/opt/claude/run/c1" {
		t.Errorf("ownRunDir = %q, want the socket's directory", c.ownRunDir)
	}
	if c.selfPid != os.Getpid() {
		t.Errorf("selfPid = %d, want %d", c.selfPid, os.Getpid())
	}
	// selfStart is the pid-reuse-proof half of "never act on ourselves". A mutant that
	// skipped the hcReadIdent read leaves it empty and the cleaner can no longer tell its
	// own pid from a reused one.
	if c.selfStart != "4242" {
		t.Errorf("selfStart = %q, want the start-ticks from our own stat", c.selfStart)
	}
	if c.roots.daemonBin != "server" || len(c.roots.roots) != 1 || c.roots.roots[0] != "/opt/claude" {
		t.Errorf("roots = %+v, want one root /opt/claude with daemonBin server", c.roots)
	}

	// A socket that is not run-dir shaped, and an executable that is not a deployed daemon,
	// are both refusals rather than cleaners with no containment.
	if _, err := newHostCleaner("/tmp/loose/rpc.sock"); err == nil {
		t.Error("a socket outside a run dir produced a cleaner")
	}
}

// TestStartHostCleaner drives the real entry point: the empty-socket no-op, the refusal when
// the roots cannot be derived, and one full turn of each background loop. hcSleep parks both
// goroutines at their first interval sleep, so exactly one keepalive and one sweep run.
func TestStartHostCleaner(t *testing.T) {
	root, _, _ := fakeProc(t)
	hcFakeSelf(t, root, "/opt/claude/srv/abc123/server", "4242")

	oldSleep, oldChtimes, oldDial := hcSleep, hcChtimes, hcDial
	t.Cleanup(func() { hcSleep, hcChtimes, hcDial = oldSleep, oldChtimes, oldDial })

	var chtimes, dials atomic.Int32
	hcChtimes = func(string, time.Time, time.Time) error { chtimes.Add(1); return nil }
	// The sweep's first act is the own-socket probe; answering ENOENT makes Pass bail at
	// once, so the loop body runs without the cleaner touching anything.
	hcDial = func(string) (net.Conn, error) {
		dials.Add(1)
		return nil, &net.OpError{Op: "dial", Err: syscall.ENOENT}
	}
	// Start's two loops have no cancellation path — they are fire-and-forget in production —
	// so the stub has to end them itself. Parking them in a bare select{} would leak two
	// goroutines per run of this test, which at -count=N accumulates. Instead each parks on
	// stop, and runtime.Goexit ends the goroutine from inside the loop when the test closes
	// it. Measured: goroutines no longer grow across repeated runs.
	parked := make(chan struct{}, 4)
	stopped := make(chan struct{}, 4)
	stop := make(chan struct{})
	t.Cleanup(func() {
		close(stop)
		for i := 0; i < 2; i++ {
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Errorf("only %d of the 2 cleaner goroutines exited after the stop signal", i)
				return
			}
		}
	})
	hcSleep = func(d time.Duration) {
		if d == hcPassDelay {
			return // the sweep loop's one-off delay before its first pass
		}
		parked <- struct{}{} // both loops reach their interval sleep exactly once
		<-stop
		stopped <- struct{}{}
		runtime.Goexit()
	}

	// An empty socket starts nothing. This arm is a redundant fast path rather than a
	// behaviour: deriveRoots cleans "" to ".", whose basename is never rpc.sock, so removing
	// the guard still refuses — just one hcSelfExe read later. The assertion is the contract,
	// not a mutant oracle.
	startHostCleaner("")
	if chtimes.Load() != 0 || dials.Load() != 0 {
		t.Errorf("an empty socket started the loops (chtimes=%d dials=%d)", chtimes.Load(), dials.Load())
	}
	startHostCleaner("/tmp/loose/rpc.sock")
	if chtimes.Load() != 0 || dials.Load() != 0 {
		t.Errorf("a socket outside a run dir started the loops (chtimes=%d dials=%d)", chtimes.Load(), dials.Load())
	}

	startHostCleaner("/opt/claude/run/c1/rpc.sock")
	for i := 0; i < 2; i++ {
		select {
		case <-parked:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of the 2 loops reached its interval sleep", i)
		}
	}
	// Receiving from parked orders both goroutines' last read of these seams before the
	// cleanup restores them.
	if chtimes.Load() == 0 {
		t.Error("the keepalive loop never touched the run dir")
	}
	if dials.Load() == 0 {
		t.Error("the sweep loop never ran a pass")
	}
}

// TestKeepaliveLogsAChtimesFailure covers the error arm of the keepalive. Its only observable
// is the log line, so the log is what the test asserts: the run dir whose mtime could not be
// bumped must be named, because that dir is the one a later tidy pass could then retire.
func TestKeepaliveLogsAChtimesFailure(t *testing.T) {
	oldChtimes, oldClock := hcChtimes, hcClock
	t.Cleanup(func() { hcChtimes, hcClock = oldChtimes, oldClock })
	hcClock = time.Now
	hcChtimes = func(string, time.Time, time.Time) error { return syscall.EIO }

	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })

	c := &hostCleaner{ownRunDir: "/opt/claude/run/self"}
	c.Keepalive()

	got := buf.String()
	if !strings.Contains(got, "/opt/claude/run/self") || !strings.Contains(got, "input/output error") {
		t.Errorf("keepalive log = %q, want the run dir and the underlying error", got)
	}
}

// TestHcSignalPidRealBody exercises the seam's own implementation, which every other test
// replaces. Signal 0 delivers nothing and only reports whether the pid can be signalled, so
// the assertions are safe: this process is signallable, a reaped child's pid is not. Without
// the ESRCH half a stub body of `return nil` would satisfy the test.
func TestHcSignalPidRealBody(t *testing.T) {
	if err := hcSignalPid(os.Getpid(), 0); err != nil {
		t.Errorf("hcSignalPid(self, 0) = %v, want nil", err)
	}

	// A pid above pid_max can never exist, so this arm is race-free: it is the one that has
	// to hold for the test to be an oracle at all.
	if err := hcSignalPid(1<<30, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("hcSignalPid(impossible pid, 0) = %v, want ESRCH", err)
	}

	// The same answer for a pid that existed and was reaped, which is the shape the cleaner
	// actually meets. This one IS racy — the kernel could hand that number to a new process
	// between the reap and the call — so a non-ESRCH answer skips rather than fails. The
	// arm above is what keeps the test honest if that ever happens.
	exe, env := helperCommand(t, "exit:0")
	cmd := exec.Command(exe)
	cmd.Env = buildEnv(env)
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	dead := cmd.Process.Pid
	if _, err := os.Stat("/proc/" + strconv.Itoa(dead)); err == nil {
		t.Skipf("pid %d was reused before the assertion", dead)
	}
	if err := hcSignalPid(dead, 0); !errors.Is(err, syscall.ESRCH) {
		t.Skipf("hcSignalPid(reaped pid %d, 0) = %v, not ESRCH: the pid was reused mid-test", dead, err)
	}
}

// TestEndDaemonsNoTargets: an empty target list returns before anything else runs. The
// summary and the signal lists alone cannot prove that — a mutant without the guard walks an
// empty loop and answers identically — so the test also asserts the clock is never read,
// which is the first thing the body past the guard does (twice, for the two wait deadlines).
func TestEndDaemonsNoTargets(t *testing.T) {
	root, _, _ := fakeProc(t)
	single, group := hcSeamSignals(t, root) // also seams the clock, which we override below
	oldClock := hcClock
	t.Cleanup(func() { hcClock = oldClock })
	clocks := 0
	hcClock = func() time.Time { clocks++; return time.Unix(2_000_000, 0) }

	var sum hcSummary
	(&hostCleaner{}).endDaemons(nil, &sum)

	if sum != (hcSummary{}) {
		t.Errorf("summary = %+v, want the zero summary for an empty target list", sum)
	}
	if len(*single) != 0 || len(*group) != 0 {
		t.Errorf("signals sent for an empty target list: single=%v group=%v", *single, *group)
	}
	if clocks != 0 {
		t.Errorf("endDaemons read the clock %d times for an empty target list; it should have returned first", clocks)
	}
}

// TestEndGroupsTwoPhaseSignalArms covers the two arms that decide NOT to escalate.
func TestEndGroupsTwoPhaseSignalArms(t *testing.T) {
	t.Run("a group that is already gone is not counted as signalled", func(t *testing.T) {
		root, mk, _ := fakeProc(t)
		oldGrp, oldSig, oldClock, oldSleep := killGroup, hcSignalPid, hcClock, hcSleep
		t.Cleanup(func() { killGroup, hcSignalPid, hcClock, hcSleep = oldGrp, oldSig, oldClock, oldSleep })
		now := time.Unix(2_000_000, 0)
		hcClock = func() time.Time { return now }
		hcSleep = func(d time.Duration) { now = now.Add(d) }
		// The process is still in the fake tree (so the pre-signal identity check passes),
		// but the kernel says it is gone by the time the signal lands.
		mk(7001, "stat", hcRunningStat(7001, 1, 7001, "1"))
		killGroup = func(int, syscall.Signal) error { return syscall.ESRCH }
		hcSignalPid = func(int, syscall.Signal) error { return syscall.ESRCH }

		signalled, survived := endGroupsTwoPhase([]tracked{{pid: 7001, pgid: 7001, startTicks: "1"}}, "orphaned process group")
		if signalled != 0 || survived != 0 {
			t.Errorf("signalled=%d survived=%d, want 0 and 0 for an already-gone group", signalled, survived)
		}
		_ = root
	})

	t.Run("a pid reused during the grace is never SIGKILLed", func(t *testing.T) {
		root, mk, _ := fakeProc(t)
		oldGrp, oldSig, oldClock, oldSleep := killGroup, hcSignalPid, hcClock, hcSleep
		t.Cleanup(func() { killGroup, hcSignalPid, hcClock, hcSleep = oldGrp, oldSig, oldClock, oldSleep })

		const pid = 7002
		mk(pid, "stat", hcRunningStat(pid, 1, pid, "1"))

		var sent []syscall.Signal
		killGroup = func(_ int, sig syscall.Signal) error {
			if sig == 0 { // the hcGroupExists probe
				return nil
			}
			sent = append(sent, sig)
			return nil // SIGTERM lands but the process does not exit
		}
		hcSignalPid = func(_ int, sig syscall.Signal) error { sent = append(sent, sig); return nil }

		// The wait reads the clock once for the deadline, then once per poll after the
		// liveness checks. One sleep jumps the whole grace, so the third read is the one
		// that ends the wait — and rewriting the stat there is exactly the race the arm
		// defends against: the pid is reused after the final liveness check and before the
		// SIGKILL would land.
		now := time.Unix(2_000_000, 0)
		hcSleep = func(time.Duration) { now = now.Add(hcTermGrace) }
		clocks, reused := 0, false
		hcClock = func() time.Time {
			clocks++
			if clocks == 3 { // 1: the grace deadline. 2: poll one. 3: the check that returns.
				reused = true
				mk(pid, "stat", hcRunningStat(pid, 1, pid, "9999")) // a different process now
			}
			return now
		}

		signalled, survived := endGroupsTwoPhase([]tracked{{pid: pid, pgid: pid, startTicks: "1"}}, "orphaned process group")
		if !reused {
			t.Fatal("the fixture never reused the pid; endGroupsTwoPhase's clock reads no longer line up")
		}
		if signalled != 1 {
			t.Errorf("signalled = %d, want 1 (the SIGTERM did land)", signalled)
		}
		if survived != 0 {
			t.Errorf("survived = %d, want 0: a reused pid is dropped, not counted", survived)
		}
		for _, sig := range sent {
			if sig == syscall.SIGKILL {
				t.Errorf("SIGKILL was sent to a reused pid (signals: %v)", sent)
			}
		}
		_ = root
	})
}
