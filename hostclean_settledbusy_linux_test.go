//go:build linux

package main

import (
	"testing"
	"time"
)

// Two things are pinned here.
//
// First, linux must sample at all. claustrum's linux hcSettledBusy was a constant
// false, so the "idle daemon still shows a live connection" spare in
// retireAbandoned could never fire there. The reference's own equivalent is
// reached from its retireAbandoned on linux, and claustrum has a real linux
// hcBusy to sample with.
//
// Second, the sampling rule: sample for a 3-second window, take at least 2
// samples, and return at once on the first sample that says not busy. The three
// values match reference build 90fca6e6 and carry the pointer-class label on the
// const block in hostclean.go: read from that build, not probe-measured.

// busyFakeProc points procRoot at a tree in which pid 42 has a connected client on
// its own unix socket, which is what hcBusy looks for.
func busyFakeProc(t *testing.T) {
	t.Helper()
	_, mk, link := fakeProc(t)
	link(42, "fd/4", "socket:[555]")
	mk(42, "net/unix", "Num RefCount Protocol Flags Type St Inode Path\n"+
		"0000: 00000002 00000000 00000000 0001 03 555 /opt/claude/run/x/rpc.sock\n")
}

// TestHcSettledBusyLinuxSamples pins that linux samples rather than answering a
// constant. The control is the first assertion: a process with no connected client
// must still read as not settled busy.
func TestHcSettledBusyLinuxSamples(t *testing.T) {
	oldSleep, oldClock := hcSleep, hcClock
	t.Cleanup(func() { hcSleep, hcClock = oldSleep, oldClock })
	// The clock is driven by the sleeps, not left real. A no-op hcSleep with a real
	// clock makes the busy case hot-spin for the full window: measured at 3.00s of
	// wall time, in a race suite the repo deliberately keeps near a minute.
	now := time.Unix(1_700_000_000, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }

	// The control. pid 99 has nothing under the fake /proc at all.
	_, _, _ = fakeProc(t)
	if hcSettledBusy(99) {
		t.Fatal("a process with no connected client read as settled busy")
	}

	// The new arm. Before this change claustrum answered false here, whatever
	// /proc said.
	busyFakeProc(t)
	if !hcSettledBusy(42) {
		t.Error("a process with a connected client did not read as settled busy")
	}
}

// TestHcSettledBusyHonoursTheDeadline pins the 90fca6e6 sampling rule: at least two
// samples, and sampling continues until the deadline passes.
func TestHcSettledBusyHonoursTheDeadline(t *testing.T) {
	oldSleep, oldClock := hcSleep, hcClock
	t.Cleanup(func() { hcSleep, hcClock = oldSleep, oldClock })

	// A clock the sleeps drive, so the test never really waits. Each sleep advances
	// it by the slept amount.
	now := time.Unix(1_700_000_000, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }

	busyFakeProc(t)
	start := now
	if !hcSettledBusy(42) {
		t.Fatal("a busy process did not read as settled busy")
	}
	elapsed := now.Sub(start)

	// claustrum's own previous rule sampled for about 900 ms, so a fixture that
	// only checked "more than one sample" would pass under both.
	if elapsed < hcBusyWindow {
		t.Errorf("sampled for %v, want at least the %v window", elapsed, hcBusyWindow)
	}
	// And it must not run away: the deadline ends it on the first sample past the
	// window.
	if elapsed > hcBusyWindow+time.Second {
		t.Errorf("sampled for %v, want the loop to stop soon after %v", elapsed, hcBusyWindow)
	}
}

// TestHcSettledBusyStopsOnTheFirstIdleSample pins the early return. A single not
// busy sample ends the loop, so an idle daemon is never held for the full window.
func TestHcSettledBusyStopsOnTheFirstIdleSample(t *testing.T) {
	oldSleep, oldClock := hcSleep, hcClock
	t.Cleanup(func() { hcSleep, hcClock = oldSleep, oldClock })
	now := time.Unix(1_700_000_000, 0)
	hcClock = func() time.Time { return now }
	hcSleep = func(d time.Duration) { now = now.Add(d) }

	// pid 42 is busy, pid 99 is not. Sampling 99 must return at once.
	busyFakeProc(t)
	start := now
	// This first assertion is a control, not the subject: a sampler that always
	// answered false would pass it. The elapsed check below is what this test owns.
	if hcSettledBusy(99) {
		t.Fatal("an idle process read as settled busy")
	}
	if elapsed := now.Sub(start); elapsed > time.Second {
		t.Errorf("an idle process took %v to decide, want an immediate return", elapsed)
	}
}

// TestHcSettledBusyTakesTwoSamples pins hcBusyMin, which nothing else does.
//
// The deadline is computed once on entry, and every other fixture here advances
// the clock only inside hcSleep, so the first sample is never already past it and
// the minimum never changes an answer. Delete the minimum and the whole suite
// still passes. That was measured, not guessed: a mutant dropping the clause went
// green.
//
// This fixture makes the first sample land past the deadline, by advancing the
// clock on every read. The minimum is then the only thing stopping a single
// sample from settling the question. It is not decorative on darwin, where hcBusy
// shells out to lsof and one sample can genuinely take seconds.
func TestHcSettledBusyTakesTwoSamples(t *testing.T) {
	oldSleep, oldClock := hcSleep, hcClock
	t.Cleanup(func() { hcSleep, hcClock = oldSleep, oldClock })

	now := time.Unix(1_700_000_000, 0)
	// Past the window on the very first read, so only hcBusyMin holds the loop.
	hcClock = func() time.Time { now = now.Add(hcBusyWindow + time.Second); return now }
	sleeps := 0
	hcSleep = func(d time.Duration) { sleeps++; now = now.Add(d) }

	busyFakeProc(t)
	if !hcSettledBusy(42) {
		t.Fatal("a busy process did not read as settled busy")
	}
	if sleeps == 0 {
		t.Error("settled after a single sample; the rule takes at least two")
	}
}

// TestRetireAbandonedSparesABusyDaemon drives the spare arm through retireAbandoned
// itself, not through hcSettledBusy alone.
//
// The four tests above call the sampler directly, so none of them reaches the
// `if hcSettledBusy(pid)` branch in retireAbandoned. On linux that branch was dead
// before this sampler became real, and a comment in hostclean_tidy_linux_test.go
// used to list it among the arms no fixture can reach.
//
// The control runs first and must retire. Without it an implementation that refused
// every candidate would pass the spare arm.
func TestRetireAbandonedSparesABusyDaemon(t *testing.T) {
	const pid = 4300
	const socket = "/opt/claude/run/x/rpc.sock"

	// run plants one verifiable, old, marked daemon and says whether it keeps a
	// connected client for every sample.
	run := func(t *testing.T, busy bool) (retired bool, signalled []int) {
		t.Helper()
		proot, mk, link := fakeProc(t)
		single, _ := hcSeamSignals(t, proot) // seams the clock, the sleep and both signals
		oldUID := hcGetuid
		t.Cleanup(func() { hcGetuid = oldUID })
		hcGetuid = func() int { return 1000 }

		exe := "/opt/claude/srv/a/server"
		hcFakeDaemon(t, proot, mk, link, pid, exe,
			[]string{exe, "--serve", "--socket", socket}, true, "1")
		link(pid, "fd/4", "socket:[555]")
		peers := "Num RefCount Protocol Flags Type St Inode Path\n"
		if busy {
			peers += "0000: 00000002 00000000 00000000 0001 03 555 " + socket + "\n"
		}
		mk(pid, "net/unix", peers)

		c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}, selfPid: 999999}
		return c.retireAbandoned(pid, socket), *single
	}

	t.Run("control: an idle daemon is retired", func(t *testing.T) {
		retired, signalled := run(t, false)
		if !retired {
			t.Fatal("an idle, verifiable daemon was not retired")
		}
		if len(signalled) != 1 || signalled[0] != pid {
			t.Fatalf("signalled = %v, want [%d]", signalled, pid)
		}
	})

	t.Run("a daemon busy across the window is spared", func(t *testing.T) {
		retired, signalled := run(t, true)
		if len(signalled) != 0 {
			t.Errorf("signalled = %v, want nothing: the daemon is still serving someone", signalled)
		}
		if retired {
			t.Error("retireAbandoned reported a retirement it did not make")
		}
	})
}
