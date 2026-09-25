//go:build linux

package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// retireAbandoned holds a bare pid across two checks that can take seconds: the busy
// sampler, which runs for up to hcBusyWindow, and verifyListener, which dials the
// socket. A daemon that exits in that span can have its pid taken by an unrelated
// process, so the SIGTERM afterwards needs the identity captured on entry, not the
// number alone.
//
// This is the window claustrum's sampling change widened: its old debounce
// ran about 900 ms, and on linux it did not run at all.

// retireReuseFixture builds one verifiable, old, marked daemon that shows a connected
// client on the first busy sample and none on the second, so the sampler sleeps exactly
// once and the swap lands inside the window. When reuse is true the pid comes back as a
// different process during that sleep: same socket and same marks, different start-ticks.
//
// It returns retireAbandoned's answer and every pid that was signalled.
func retireReuseFixture(t *testing.T, reuse bool) (retired bool, signalled []int) {
	t.Helper()
	const pid = 4200
	const socket = "/opt/claude/run/x/rpc.sock"

	proot, mk, link := fakeProc(t)
	single, _ := hcSeamSignals(t, proot) // seams the clock, the sleep and both signals
	oldUID, seamedSleep := hcGetuid, hcSleep
	t.Cleanup(func() { hcGetuid, hcSleep = oldUID, seamedSleep })
	hcGetuid = func() int { return 1000 }

	exe := "/opt/claude/srv/a/server"
	argv := []string{exe, "--serve", "--socket", socket}
	hcFakeDaemon(t, proot, mk, link, pid, exe, argv, true, "1")

	// A connected client, which is what hcBusy looks for.
	link(pid, "fd/4", "socket:[555]")
	mk(pid, "net/unix", "Num RefCount Protocol Flags Type St Inode Path\n"+
		"0000: 00000002 00000000 00000000 0001 03 555 "+socket+"\n")

	swapped := false
	hcSleep = func(d time.Duration) {
		seamedSleep(d) // the seamed sleep advances the fake clock
		if swapped {
			return
		}
		swapped = true
		// The client goes away, so the next sample reads idle and the sampler returns.
		mk(pid, "net/unix", "Num RefCount Protocol Flags Type St Inode Path\n")
		if reuse {
			// A different process now holds the number. Only the start-ticks differ,
			// so nothing before the signal can tell it apart by pid alone.
			mk(pid, "stat", strconv.Itoa(pid)+" (server) R 1 "+strconv.Itoa(pid)+" "+
				strings.Repeat("0 ", 16)+"2 x")
		}
	}

	c := &hostCleaner{roots: &hostRoots{roots: []string{"/opt/claude"}, daemonBin: "server"}, selfPid: 999999}
	retired = c.retireAbandoned(pid, socket)
	if !swapped {
		t.Fatal("the sampler never slept, so the fixture never reached the window it is about")
	}
	return retired, *single
}

// TestRetireAbandonedRefusesAPidReusedDuringSampling pins the identity re-check.
//
// The control runs first and must retire. Without it a retireAbandoned that refused
// every pid would pass the reuse arm, and so would one that never reached the signal
// because the fixture broke somewhere earlier.
func TestRetireAbandonedRefusesAPidReusedDuringSampling(t *testing.T) {
	t.Run("control: the same process throughout is retired", func(t *testing.T) {
		retired, signalled := retireReuseFixture(t, false)
		if !retired {
			t.Fatal("an idle, verifiable daemon was not retired")
		}
		if len(signalled) != 1 || signalled[0] != 4200 {
			t.Fatalf("signalled = %v, want [4200]", signalled)
		}
	})

	t.Run("a pid reused during sampling is not signalled", func(t *testing.T) {
		retired, signalled := retireReuseFixture(t, true)
		if len(signalled) != 0 {
			t.Errorf("signalled = %v, want nothing: the pid belongs to another process now", signalled)
		}
		// The answer matters as much as the signal: true counts a retirement in the
		// summary and lets the tidy pass go on to remove the run dir.
		if retired {
			t.Error("retireAbandoned reported a retirement it did not make")
		}
	})
}
