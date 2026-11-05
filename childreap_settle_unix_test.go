//go:build linux || darwin

package main

import (
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestReapTargetsUnknownAfterSettlePause covers a leader that reads as gone at the test
// before the SIGKILL, is alive at the read after the settle pause, and then cannot be
// inspected at its last test. The group gets no SIGKILL, its record stays for the next
// start, and it counts as kept. Every read and every signal goes through a seam.
func TestReapTargetsUnknownAfterSettlePause(t *testing.T) {
	sim, runDir, tg := simTarget(t)
	sim.diesOn(tg.pid, syscall.SIGKILL)
	start := sim.now
	// step 0: the grace. step 1: the next read says gone. step 2: the settle read
	// (no environment) sees the leader, and a full read cannot inspect it.
	step := 0
	reapNow = func() time.Time {
		if step == 0 && sim.now.Sub(start) >= reapGrace {
			step = 1
		}
		return sim.now
	}
	inner := readLiveProc
	readLiveProc = func(pid int, wantEnv bool) liveProc {
		switch {
		case step == 1:
			step = 2
			return liveProc{state: procGone}
		case step == 2 && wantEnv:
			return liveProc{state: procUnknown, why: "unparseable stat"}
		}
		return inner(pid, wantEnv)
	}
	var counts reapCounts
	out := captureLog(t, func() { reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts) })

	if step != 2 {
		t.Fatalf("step = %d, want the reads to reach the test after the settle pause", step)
	}
	if sim.sigCount(syscall.SIGTERM) != 1 || sim.sigCount(syscall.SIGKILL) != 0 {
		t.Errorf("kills = %v, want one SIGTERM and no SIGKILL", sim.kills)
	}
	if !recordExists(runDir, tg.name) {
		t.Error("the record is gone, want it kept for the next start")
	}
	if want := (reapCounts{signalled: 1, skipped: 1}); counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
	last := "[process.Registry] group 1073742825 could still not be inspected when the wait was up; keeping its record for the next start\n"
	if strings.Count(out, last) != 1 {
		t.Errorf("log = %q\nwant it to hold %q once", out, last)
	}
}
