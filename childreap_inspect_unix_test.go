//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// The tests of a process that the reap cannot inspect. Every read and every signal
// goes through a seam, so no real process gets a signal.

// TestReapWaitKeepsGroupItCannotInspect is cell H4 (`5fd08069`, Linux VM): a group that
// got SIGTERM and then cannot be inspected gets no SIGKILL, and its record stays. The
// two lines are the reference's. The control case still passes the record test and
// gets the SIGKILL at the end of the grace.
func TestReapWaitKeepsGroupItCannotInspect(t *testing.T) {
	for _, tc := range []struct {
		name      string
		inspected bool
	}{
		{"the stat no longer parses", false},
		{"control: the leader still passes", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sim, runDir, tg := simTarget(t)
			sim.onKill[tg.pid] = func(sig syscall.Signal) error {
				switch {
				case sig == syscall.SIGKILL:
					delete(sim.live, tg.pid)
				case !tc.inspected:
					sim.live[tg.pid] = &liveProc{state: procUnknown, why: "unparseable stat"}
				}
				return nil
			}
			start := sim.now
			var counts reapCounts
			out := captureLog(t, func() { reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts) })

			if sim.sigCount(syscall.SIGTERM) != 1 {
				t.Errorf("kills = %v, want exactly one SIGTERM", sim.kills)
			}
			if tc.inspected {
				if sim.sigCount(syscall.SIGKILL) != 1 || recordExists(runDir, tg.name) {
					t.Errorf("control: kills = %v, record there = %v, want one SIGKILL and no record", sim.kills, recordExists(runDir, tg.name))
				}
				return
			}
			if sim.sigCount(syscall.SIGKILL) != 0 {
				t.Errorf("kills = %v, want no SIGKILL", sim.kills)
			}
			if !recordExists(runDir, tg.name) {
				t.Error("the record is gone, want it kept for the next start")
			}
			first := "[process.Registry] group 1001 cannot be inspected now (unparseable stat); looking again until the wait is up, sending nothing more unless it verifies again or is seen gone\n"
			last := "[process.Registry] group 1001 could still not be inspected when the wait was up; keeping its record for the next start\n"
			if strings.Count(out, first) != 1 || strings.Count(out, last) != 1 || strings.Index(out, first) > strings.Index(out, last) {
				t.Errorf("log = %q\nwant each of the two lines once, in order", out)
			}
			if want := (reapCounts{signalled: 1, skipped: 1}); counts != want {
				t.Errorf("counts = %+v, want %+v", counts, want)
			}
			if waited := sim.now.Sub(start); waited < reapGrace {
				t.Errorf("the wait ended after %v, want the whole %v grace", waited, reapGrace)
			}
		})
	}
}

// TestReapWaitGroupThatVerifiesAgain: a group that cannot be inspected at one poll and
// passes the record test again later gets the SIGKILL at the end of the grace. The
// first line says so: "sending nothing more unless it verifies again".
func TestReapWaitGroupThatVerifiesAgain(t *testing.T) {
	sim, runDir, tg := simTarget(t)
	good := *sim.live[tg.pid]
	reads := 0
	readLiveProc = func(pid int, wantEnv bool) liveProc {
		lp, ok := sim.live[pid]
		if !ok {
			return liveProc{state: procGone}
		}
		if reads++; reads == 2 { // the first poll after the SIGTERM
			return liveProc{state: procUnknown, why: "unparseable stat"}
		}
		return *lp
	}
	sim.live[tg.pid] = &good
	sim.diesOn(tg.pid, syscall.SIGKILL)
	var counts reapCounts
	out := captureLog(t, func() { reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts) })

	if sim.sigCount(syscall.SIGKILL) != 1 || counts.reapedEscalate != 1 || counts.skipped != 0 {
		t.Errorf("kills = %v, counts = %+v, want one SIGKILL and one group in the escalate bucket", sim.kills, counts)
	}
	if !strings.Contains(out, "cannot be inspected now (unparseable stat); looking again") || strings.Contains(out, "could still not be inspected") {
		t.Errorf("log = %q\nwant the first line only", out)
	}
}

// TestSameLeaderCannotInspect: the test right before a signal gives no go for a process
// that cannot be inspected, or that is not this daemon's to judge.
func TestSameLeaderCannotInspect(t *testing.T) {
	const pid = 700
	for _, tc := range []struct {
		name string
		live liveProc
		want int
	}{
		{"cannot inspect", liveProc{state: procUnknown, why: "unparseable stat"}, verdictUnknown},
		{"not ours", liveProc{state: procNotOurs}, verdictUnknown},
		{"not a child", liveProc{state: procNotChild}, verdictSkip},
		{"namespace link gone", liveProc{state: procAlive, startTicks: "100", pgid: pid, nsState: procGone}, verdictGone},
		{"namespace link unreadable", liveProc{state: procAlive, startTicks: "100", pgid: pid, nsState: procNotChild}, verdictSkip},
		{"control", liveProc{state: procAlive, startTicks: "100", pgid: pid}, verdictReap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeLiveProcs(t, map[int]liveProc{pid: tc.live})
			if got, _ := sameLeader(pid, "100"); got != tc.want {
				t.Errorf("sameLeader = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestReapTargetsCannotInspectBeforeSIGTERM: a target that cannot be inspected at the
// test right before the SIGTERM gets no signal, and its record stays. The control is
// TestReapWaitKeepsGroupItCannotInspect, where the same target gets its SIGTERM.
func TestReapTargetsCannotInspectBeforeSIGTERM(t *testing.T) {
	sim, runDir, tg := simTarget(t)
	sim.live[tg.pid] = &liveProc{state: procUnknown, why: "unparseable stat"}
	var counts reapCounts
	reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts)
	if len(sim.kills) != 0 || !recordExists(runDir, tg.name) || counts != (reapCounts{skipped: 1}) {
		t.Errorf("kills = %v, record there = %v, counts = %+v, want no signal, the record and one kept", sim.kills, recordExists(runDir, tg.name), counts)
	}
}

// TestDaemonAliveCannotInspect: a recorded daemon that cannot be inspected counts as
// alive (`5fd08069`, cells H2a to H2c, Linux VM). TestDaemonAlive has the gone case.
func TestDaemonAliveCannotInspect(t *testing.T) {
	for _, state := range []int{procUnknown, procNotOurs} {
		fakeLiveProcs(t, map[int]liveProc{800: {state: state}})
		if !daemonAlive(800, "5", 42) {
			t.Errorf("daemonAlive = false for state %d, want true", state)
		}
	}
}

// TestNotLockHolderReapsAndRecordsNothing is cell H5 (`5fd08069`, Linux VM): a daemon
// that does not hold the run-dir lock runs no reap and writes no child record. The test
// holds the lock itself, so nothing is there to signal. The control is the same start
// with a free lock: the recorded group gets its SIGTERM and the daemon writes a record.
func TestNotLockHolderReapsAndRecordsNothing(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	const orphan = 800000401
	for _, tc := range []struct {
		name   string
		holder bool
	}{
		{"another process holds the lock", false},
		{"control: the lock is free", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortTempDir(t)
			sock := filepath.Join(dir, "rpc.sock")
			if !tc.holder {
				fd, err := syscall.Open(filepath.Join(dir, runDirLockName), syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = syscall.Close(fd) })
				if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatalf("flock: %v", err)
				}
				oldHold := holdHolder
				t.Cleanup(func() { holdHolder = oldHold })
				holdHolder = func(int) (func(syscall.Signal) error, func()) {
					return func(syscall.Signal) error { return syscall.ESRCH }, func() {}
				}
			}
			name := writeRec(t, dir, orphanRecord(orphan, "/n", ""))
			sim := newProcSim(t)
			sim.live[orphan] = &liveProc{state: procAlive, startTicks: "9000", pgid: orphan,
				program: "/n", runDir: dir, childMark: strconv.Itoa(orphan) + ":9000"}
			sim.diesOn(orphan, syscall.SIGTERM)

			s, err := newServerOnSocket(sock, "tok", "", wireLogOptions{}, false, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })
			// The record of this test process stands for the record of a spawned child.
			own := strconv.Itoa(os.Getpid()) + ".json"
			s.procs.recordChild(os.Getpid(), "/n", "")
			t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, "children", own)) })

			if tc.holder {
				if sim.sigCount(syscall.SIGTERM) != 1 || recordExists(dir, name) || !recordExists(dir, own) {
					t.Errorf("control: kills = %v, old record there = %v, new record there = %v, want one SIGTERM, no old record and a new record",
						sim.kills, recordExists(dir, name), recordExists(dir, own))
				}
				return
			}
			if len(sim.kills) != 0 {
				t.Errorf("kills = %v, want no signal from a daemon without the lock", sim.kills)
			}
			if !recordExists(dir, name) {
				t.Error("the record of the predecessor is gone, want it left alone")
			}
			if recordExists(dir, own) {
				t.Error("a daemon without the lock wrote a child record")
			}
		})
	}
}
