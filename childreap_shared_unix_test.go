//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// The fakes that the reap tests share on linux and darwin. They seam every read of a
// live process and every signal, so no test here signals a real process.

// simOwnDaemon, simOwnParent and simOwnPgid stand for this daemon's own pid, parent pid
// and group in a test that calls reapTargets or verifyOrphan directly. No fake process
// uses one of them.
const simOwnDaemon, simOwnParent, simOwnPgid = 10, 11, 12

// reapOrphans runs the startup reap of a manager on runDir, as the daemon does.
func reapOrphans(runDir, ownInstance string) {
	(&procManager{runDir: runDir, instanceID: ownInstance}).reapOrphans()
}

// simCtx is the sweep context of a test that calls reapTargets or reapWait directly.
// The children folder of runDir must exist.
func simCtx(t *testing.T, runDir string) *reapCtx {
	t.Helper()
	dir, err := os.OpenRoot(filepath.Join(runDir, "children"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return &reapCtx{dir: dir, runDir: runDir, ownDaemonPid: simOwnDaemon, ownParentPid: simOwnParent, ownPgid: simOwnPgid}
}

// fakeLiveProcs seams readLiveProc at a fixed table keyed by pid; an unlisted pid reads
// as gone. It also silences reapSleep so settleThenVerdict's 50ms settle does not slow the test.
func fakeLiveProcs(t *testing.T, table map[int]liveProc) {
	t.Helper()
	oldRead, oldSleep := readLiveProc, reapSleep
	t.Cleanup(func() { readLiveProc, reapSleep = oldRead, oldSleep })
	readLiveProc = func(pid int, wantEnv bool) liveProc {
		if lp, ok := table[pid]; ok {
			return lp
		}
		return liveProc{state: procGone}
	}
	reapSleep = func(time.Duration) {}
}

type killRec struct {
	pid int
	sig syscall.Signal
}

// procSim is a stateful fake for the wait tests. It seams readLiveProc, killGroup,
// groupAlive, reapNow and reapSleep so a process can die (or be reused) in response to a
// real signal while the wait polls, and the clock advances only when the wait sleeps, so
// the two-phase loops terminate deterministically without real time.
type procSim struct {
	live   map[int]*liveProc
	group  map[int]bool
	onKill map[int]func(sig syscall.Signal) error
	kills  []killRec
	now    time.Time
}

func newProcSim(t *testing.T) *procSim {
	t.Helper()
	s := &procSim{
		live:   map[int]*liveProc{},
		group:  map[int]bool{},
		onKill: map[int]func(syscall.Signal) error{},
		now:    time.Now(), // realistic, so the foreign-record age check is meaningful
	}
	oldRead, oldKill, oldGroup, oldNow, oldSleep := readLiveProc, killGroup, groupAlive, reapNow, reapSleep
	t.Cleanup(func() {
		readLiveProc, killGroup, groupAlive, reapNow, reapSleep = oldRead, oldKill, oldGroup, oldNow, oldSleep
	})
	readLiveProc = func(pid int, wantEnv bool) liveProc {
		if lp, ok := s.live[pid]; ok {
			return *lp
		}
		return liveProc{state: procGone}
	}
	killGroup = func(pid int, sig syscall.Signal) error {
		s.kills = append(s.kills, killRec{pid, sig})
		if fn := s.onKill[pid]; fn != nil {
			return fn(sig)
		}
		return nil
	}
	groupAlive = func(pid int) bool { return s.group[pid] }
	reapNow = func() time.Time { return s.now }
	reapSleep = func(d time.Duration) { s.now = s.now.Add(d) }
	return s
}

// add registers a live, group-leading process with the given start-ticks.
func (s *procSim) add(pid int, start string) {
	s.live[pid] = &liveProc{state: procAlive, startTicks: start, pgid: pid}
}

// diesOn makes the process exit (and its group empty) when killGroup signals it with
// dieSig; other signals are recorded but leave it running.
func (s *procSim) diesOn(pid int, dieSig syscall.Signal) {
	s.onKill[pid] = func(sig syscall.Signal) error {
		if sig == dieSig {
			delete(s.live, pid)
			s.group[pid] = false
		}
		return nil
	}
}

func (s *procSim) sigCount(sig syscall.Signal) int {
	n := 0
	for _, k := range s.kills {
		if k.sig == sig {
			n++
		}
	}
	return n
}

func (s *procSim) signaled(pid int) bool {
	for _, k := range s.kills {
		if k.pid == pid {
			return true
		}
	}
	return false
}

// addVerified registers a live process that passes the whole record test of
// verifyOrphan for runDir, and returns the target that the sweep makes for it.
func (s *procSim) addVerified(pid int, start, runDir string) reapTarget {
	s.live[pid] = &liveProc{
		state: procAlive, startTicks: start, pgid: pid, program: "/n",
		runDir: runDir, childMark: strconv.Itoa(pid) + ":" + start,
	}
	return reapTarget{
		pid: pid, start: start, name: strconv.Itoa(pid) + ".json",
		rec: childRecord{Pid: pid, Start: start, Argv0: "/n"},
	}
}

// writeRec writes one <runDir>/children/<pid>.json record and returns its file name.
func writeRec(t *testing.T, runDir string, rec childRecord) string {
	t.Helper()
	if err := writeChildRecord(runDir, rec); err != nil {
		t.Fatalf("writeChildRecord: %v", err)
	}
	return strconv.Itoa(rec.Pid) + ".json"
}

func recordExists(runDir, name string) bool {
	_, err := os.Stat(filepath.Join(runDir, "children", name))
	return err == nil
}
