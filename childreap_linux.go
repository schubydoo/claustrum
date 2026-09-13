//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The orphan reap (reference build 19f30c46, linux). At -serve startup, right after a
// daemon claims its run dir (which evicts a still-live predecessor on the same socket),
// it reaps children that a since-exited predecessor daemon of THIS run dir left behind.
// The daemon reads the per-spawn records the child registry wrote (childRecord, written
// by writeChildRecord to <runDir>/children/<pid>.json) and, for each one whose owning
// daemon is gone, verifies the live process really is that recorded child before ending
// its process group.
//
// This path is DESTRUCTIVE: a wrong reap ends a live process. So a record is reaped only
// when every guard below holds, and the kill and the /proc reads go through package-var
// seams so unit tests exercise the whole decision without ending a real process. The
// real reap runs only from a daemon at startup.
//
// The safety invariant: reap a process only when it is (a) the same process the record
// names (its /proc start-time still matches, so the pid was not reused), (b) its own
// process group leader, (c) running the recorded program, (d) tagged with OUR run dir in
// its environment, and (e) marked as a direct daemon child (its CLAUDE_SSH_CHILD names
// its own pid and start). A record whose owning daemon is still alive, or that belongs to
// another boot or machine, is never reaped. Darwin and Windows link no reap yet (a later
// slice); reapOrphans is a no-op there (see childreap_other.go).

// reapGrace is how long the reap waits for a group to exit after SIGTERM before it
// escalates to SIGKILL. reapEscalate is how long it waits after SIGKILL. Both match
// reference build 19f30c46.
const (
	reapGrace    = 2 * time.Second
	reapEscalate = 1 * time.Second
	// reapForeignTTL is how old a record from another boot or machine must be before the
	// reap forgets it. A younger foreign record is left in place, since a live process on
	// another boot cannot be verified from here. Matches reference build 19f30c46.
	reapForeignTTL = 30 * 24 * time.Hour
)

// maxReapTargets caps how many orphans one sweep signals, matching the reference's fixed
// batch. Extra candidates are left for the next startup. A var so a test can shrink it to
// exercise the overflow path.
var maxReapTargets = 64

// verdict values from verify (the per-record decision).
const (
	verdictReap        = 0 // a verified orphan of a dead predecessor daemon of this run dir
	verdictGone        = 1 // the live process is gone; forget the record, nothing to end
	verdictSkip        = 2 // skip with a reason (logged); forget the record
	verdictPassthrough = 3 // the process disowns us; skip WITHOUT forgetting the record
)

// live process states from readLiveProc.
const (
	procAlive   = 0 // /proc/<pid> is readable and the process is running
	procGone    = 1 // /proc/<pid> is absent, or the process is a zombie or dead
	procNotOurs = 2 // /proc/<pid> is unreadable, or the process is in another pid namespace
)

// liveProc is the live /proc/<pid> state the reap reads to judge a record. The env
// fields are populated only when readLiveProc is called with wantEnv true (the full
// verify), not for the lighter start-time and leadership re-checks.
type liveProc struct {
	state      int    // procAlive, procGone, or procNotOurs
	startTicks string // /proc/<pid>/stat field 22, compared as a string to childRecord.Start
	pgid       int    // /proc/<pid>/stat field 5 (process group id)
	program    string // /proc/<pid>/cmdline argv[0]
	runDir     string // CLAUDE_SSH_RUN_DIR from /proc/<pid>/environ (wantEnv only)
	childMark  string // CLAUDE_SSH_CHILD from /proc/<pid>/environ (wantEnv only)
}

// Seams. Each is a package var so a unit test replaces it to drive the decision tree and
// the two-phase escalation with no real process, no real kill, and no real wait.
var (
	// readLiveProc reads /proc/<pid>. wantEnv also reads the program and env markers.
	readLiveProc = realReadLiveProc
	// killGroup signals the whole process group led by pid (kill(-pid, sig)), the reap's
	// only destructive call.
	killGroup = func(pid int, sig syscall.Signal) error { return syscall.Kill(-pid, sig) }
	// reapWaitExit waits up to timeout for the given pids' groups to exit and returns the
	// survivors.
	reapWaitExit = realReapWaitExit
	// reapSleep is the poll delay inside realReapWaitExit.
	reapSleep = time.Sleep
	// reapNow is the clock for the foreign-record age check and the wait deadline.
	reapNow = time.Now
	// procRoot is the /proc mount realReadLiveProc reads. A var so a test can point it at
	// a fake procfs tree and exercise the parse-error arms.
	procRoot = "/proc"
)

// reapTarget is one process the sweep decided to end. start is kept for the pid-reuse
// re-check just before the kill, and name is the record to forget once handled.
type reapTarget struct {
	pid   int
	start string
	name  string
}

// reapCounts is the tally the sweep returns, logged as a summary when any is non-zero.
type reapCounts struct {
	reaped    int // groups that exited to our SIGTERM or SIGKILL
	survived  int // groups still alive after SIGKILL and the escalate wait
	forgotten int // records deleted without a reap (gone, skipped, or stale)
	skipped   int // records left in place (owner alive, foreign and fresh, or disowning)
}

// reapOrphans is the -serve startup reap. It reads <runDir>/children, decides each record,
// ends the verified orphans in two phases, and logs a summary. It is a no-op when runDir
// is empty (a non-run-shaped socket has no registry). ownInstance is this daemon's
// instance id, used to skip this daemon's own children.
func reapOrphans(runDir, ownInstance string) {
	if runDir == "" {
		return
	}
	ownDaemonPid := os.Getpid()
	ownParentPid := os.Getppid()
	ownPgid, _ := syscall.Getpgid(ownDaemonPid)
	ownNode := nodeID()
	ownHost := "machine-id:" + machineID()

	entries, err := os.ReadDir(filepath.Join(runDir, "children"))
	if err != nil {
		return // no registry dir means nothing was ever recorded here
	}

	var counts reapCounts
	var targets []reapTarget
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		rec, ok := readChildRecord(runDir, name)
		// A record that will not parse, names an out-of-range pid, or does not sit at its
		// own "<pid>.json" path (a leftover temp name) is dropped.
		if !ok || rec.Pid < 2 || rec.Pid > maxPid || name != strconv.Itoa(rec.Pid)+".json" {
			forgetChildRecord(runDir, name)
			counts.forgotten++
			continue
		}
		if rec.Instance == ownInstance {
			counts.skipped++ // our own child; leave it running
			continue
		}
		if rec.Node == ownNode {
			// Same boot, pid namespace, and machine: a predecessor of this run dir. Reap
			// its children only once that daemon itself is gone.
			if daemonAlive(rec.DaemonPid, rec.DaemonStart, ownDaemonPid) {
				counts.skipped++
				continue
			}
			verdict, reason := verifyOrphan(rec, runDir, ownDaemonPid, ownParentPid, ownPgid)
			switch verdict {
			case verdictReap:
				if len(targets) < maxReapTargets {
					targets = append(targets, reapTarget{pid: rec.Pid, start: rec.Start, name: name})
				} else {
					counts.skipped++ // batch full; leave for the next startup
				}
			case verdictGone:
				forgetChildRecord(runDir, name)
				counts.forgotten++
			case verdictSkip:
				logInfof("[process.Reaper] child %d (%s): not reaping, %s", rec.Pid, name, reason)
				forgetChildRecord(runDir, name)
				counts.forgotten++
			case verdictPassthrough:
				logInfof("[process.Reaper] child %d (%s): %s", rec.Pid, name, reason)
				counts.skipped++ // the process disowns us; keep the record
			}
			continue
		}
		// Another boot or machine. Forget a record from an earlier boot of THIS machine,
		// and forget a foreign record only once it is past the stale window.
		if earlierBootOfThisMachine(rec.Host, rec.Node, ownHost, ownNode) {
			forgetChildRecord(runDir, name)
			counts.forgotten++
			continue
		}
		if rec.At != 0 && reapNow().Sub(time.UnixMilli(rec.At)) < reapForeignTTL {
			counts.skipped++ // foreign and fresh; cannot verify it from here
			continue
		}
		forgetChildRecord(runDir, name)
		counts.forgotten++
	}

	reapTargets(runDir, targets, &counts)

	if counts.reaped+counts.survived+counts.forgotten+counts.skipped > 0 {
		logInfof("[process.Reaper] orphan sweep: reaped %d, survived %d, forgot %d, skipped %d",
			counts.reaped, counts.survived, counts.forgotten, counts.skipped)
	}
}

// maxPid is the largest usable pid (2^31 - 1). With the `pid < 2 || pid > maxPid` test it
// yields the reference's [2, 2^31) window.
const maxPid = 1<<31 - 1

// verifyOrphan decides one record whose owning daemon is gone and whose node matches
// ours. It returns a verdict and, for a skip, a short reason for the log. The reasons are
// claustrum's own phrasing of the same conditions the reference checks.
func verifyOrphan(rec childRecord, runDir string, ownDaemonPid, ownParentPid, ownPgid int) (int, string) {
	pid := rec.Pid
	switch {
	case pid < 2 || pid > maxPid:
		return verdictSkip, "pid is out of range"
	case pid == ownDaemonPid:
		return verdictSkip, "pid is this daemon"
	case pid == ownParentPid || (ownPgid > 1 && pid == ownPgid):
		return verdictSkip, "pid is this daemon's parent or process group"
	case rec.Start == "":
		return verdictSkip, "record has no start time to verify against"
	case rec.Argv0 == "":
		return verdictSkip, "record names no program to verify against"
	}

	lp := readLiveProc(pid, true)
	switch lp.state {
	case procGone:
		return settleThenVerdict(pid, verdictGone, "")
	case procNotOurs:
		// The process disowns this daemon (another pid namespace, or unreadable). Keep the
		// record and let a daemon that owns it decide.
		return verdictPassthrough, "process is not in this daemon's namespace"
	}

	// The pid-reuse guard: the live start-time must still match the record's.
	if lp.startTicks != rec.Start {
		return settleThenVerdict(pid, verdictSkip, "the pid was reused")
	}
	if pid != lp.pgid {
		return settleThenVerdict(pid, verdictSkip, "process is not its own group leader")
	}
	if !programMatches(rec.Argv0, lp.program) {
		return settleThenVerdict(pid, verdictSkip, "process is not running the recorded program")
	}
	if lp.runDir != runDir {
		if lp.runDir == "" {
			return settleThenVerdict(pid, verdictSkip, "process has no run-dir marker")
		}
		return settleThenVerdict(pid, verdictSkip, "process belongs to another run dir")
	}
	if lp.childMark != strconv.Itoa(pid)+":"+lp.startTicks {
		return settleThenVerdict(pid, verdictSkip, "process was not started directly by a daemon")
	}
	return verdictReap, ""
}

// settleThenVerdict re-reads /proc after a 50ms pause before it returns a skip, so a
// process that exited in the meantime is reported gone rather than skipped. It passes a
// gone verdict through unchanged.
func settleThenVerdict(pid, verdict int, reason string) (int, string) {
	if verdict == verdictGone {
		return verdictGone, reason
	}
	reapSleep(50 * time.Millisecond)
	if readLiveProc(pid, false).state == procGone {
		return verdictGone, ""
	}
	return verdict, reason
}

// programMatches reports whether the live process runs the recorded program, by an exact
// match on the full argv[0] or on its basename.
func programMatches(recorded, live string) bool {
	if recorded == "" || live == "" {
		return false
	}
	return recorded == live || filepath.Base(recorded) == filepath.Base(live)
}

// daemonAlive reports whether the daemon that spawned a child is still running. A daemon
// pid below 2, or equal to this daemon's own pid (a reused slot for a different instance),
// counts as gone so the child is treated as an orphan. Otherwise it is alive only when
// /proc says so and, if the record carries a daemon start-time, that start-time still
// matches (a pid-reuse guard on the daemon itself).
func daemonAlive(daemonPid int, daemonStart string, ownDaemonPid int) bool {
	if daemonPid < 2 || daemonPid == ownDaemonPid {
		return false
	}
	lp := readLiveProc(daemonPid, false)
	if lp.state != procAlive {
		return false
	}
	if daemonStart == "" {
		return true
	}
	return lp.startTicks == daemonStart
}

// earlierBootOfThisMachine reports whether a record is from an earlier boot of THIS same
// machine: the machine ids (host) match but the boot-id part of the node differs. The node
// is "<boot-id>/<pid-namespace-inode>". A record from a different machine returns false.
func earlierBootOfThisMachine(recHost, recNode, ownHost, ownNode string) bool {
	if recHost == "" || recHost != ownHost {
		return false
	}
	recBoot, _, ok1 := strings.Cut(recNode, "/")
	ownBoot, _, ok2 := strings.Cut(ownNode, "/")
	if !ok1 || !ok2 || recBoot == "" || ownBoot == "" {
		return false
	}
	return recBoot != ownBoot
}

// reapTargets ends the collected orphans in two phases: SIGTERM the group and wait the
// grace, then SIGKILL any survivor and wait the escalate. Just before each SIGTERM it
// re-reads /proc (sameLeader), so a pid reused between the sweep and the signal is not
// killed. Every handled record is forgotten. It updates counts.
func reapTargets(runDir string, targets []reapTarget, counts *reapCounts) {
	if len(targets) == 0 {
		return
	}
	var signaled []reapTarget
	for _, t := range targets {
		switch sameLeader(t.pid, t.start) {
		case verdictGone:
			counts.reaped++ // already gone; count it and forget below
			forgetChildRecord(runDir, t.name)
		case verdictSkip:
			counts.forgotten++ // reused or no longer a group leader; do not signal
			forgetChildRecord(runDir, t.name)
		default:
			if err := killGroup(t.pid, syscall.SIGTERM); err != nil {
				counts.forgotten++
				forgetChildRecord(runDir, t.name)
				continue
			}
			signaled = append(signaled, t)
		}
	}

	survivors := waitTargets(signaled, reapGrace)
	for _, t := range diffTargets(signaled, survivors) {
		counts.reaped++
		forgetChildRecord(runDir, t.name)
	}

	for _, t := range survivors {
		_ = killGroup(t.pid, syscall.SIGKILL)
	}
	stillAlive := waitTargets(survivors, reapEscalate)
	for _, t := range diffTargets(survivors, stillAlive) {
		counts.reaped++
		forgetChildRecord(runDir, t.name)
	}
	for _, t := range stillAlive {
		counts.survived++ // outlived SIGKILL (a zombie or an uninterruptible sleep); forget it
		logInfof("[process.Reaper] child %d (%s): still present after SIGKILL", t.pid, t.name)
		forgetChildRecord(runDir, t.name)
	}
}

// waitTargets waits up to timeout for the targets' groups to exit and returns the ones
// still alive.
func waitTargets(targets []reapTarget, timeout time.Duration) []reapTarget {
	if len(targets) == 0 {
		return nil
	}
	pids := make([]int, len(targets))
	byPid := make(map[int]reapTarget, len(targets))
	for i, t := range targets {
		pids[i] = t.pid
		byPid[t.pid] = t
	}
	alive := reapWaitExit(pids, timeout)
	out := make([]reapTarget, 0, len(alive))
	for _, pid := range alive {
		out = append(out, byPid[pid])
	}
	return out
}

// diffTargets returns the targets in all that are not in the survivors set (the ones that
// exited).
func diffTargets(all, survivors []reapTarget) []reapTarget {
	live := make(map[int]struct{}, len(survivors))
	for _, t := range survivors {
		live[t.pid] = struct{}{}
	}
	var out []reapTarget
	for _, t := range all {
		if _, ok := live[t.pid]; !ok {
			out = append(out, t)
		}
	}
	return out
}

// sameLeader re-reads /proc just before a kill and reports whether the target is still the
// same, group-leading process. It returns verdictGone when the process is gone,
// verdictSkip when the pid was reused (start-time changed) or the process no longer leads
// its group, and verdictReap when it is still safe to signal.
func sameLeader(pid int, recStart string) int {
	lp := readLiveProc(pid, false)
	// A start-time mismatch is only meaningful when the live process HAS a start-time. A
	// gone process has none, and that is not a reuse; it falls through to the gone check.
	if lp.startTicks != "" && lp.startTicks != recStart {
		return verdictSkip
	}
	if lp.state == procGone {
		return verdictGone
	}
	if lp.state == procAlive && pid != lp.pgid {
		return verdictSkip
	}
	return verdictReap
}

// realReadLiveProc reads /proc/<pid> for the reap. It always reads stat (state, group id,
// and start-time). When wantEnv is set it also applies the pid-namespace guard and reads
// the program from cmdline and the two markers from environ.
func realReadLiveProc(pid int, wantEnv bool) liveProc {
	lp := liveProc{state: procGone}
	proc := procRoot + "/" + strconv.Itoa(pid)
	stat, err := os.ReadFile(proc + "/stat")
	if err != nil {
		return lp // gone
	}
	i := strings.LastIndexByte(string(stat), ')')
	if i < 0 {
		lp.state = procNotOurs
		return lp
	}
	fields := strings.Fields(string(stat)[i+1:])
	if len(fields) <= 19 {
		lp.state = procNotOurs
		return lp
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return lp // zombie or dead: gone
	}
	lp.pgid, _ = strconv.Atoi(fields[2]) // field 5 (pgrp)
	lp.startTicks = fields[19]           // field 22 (starttime)
	lp.state = procAlive
	if !wantEnv {
		return lp
	}
	// The pid-namespace guard: a process in another namespace is not ours to judge.
	if self, e1 := os.Readlink(procRoot + "/self/ns/pid"); e1 == nil {
		if other, e2 := os.Readlink(proc + "/ns/pid"); e2 == nil && self != other {
			lp.state = procNotOurs
			return lp
		}
	}
	cmdline, err := os.ReadFile(proc + "/cmdline")
	if err != nil {
		lp.state = procNotOurs
		return lp
	}
	if len(cmdline) == 0 {
		lp.state = procGone
		return lp
	}
	if j := strings.IndexByte(string(cmdline), 0); j >= 0 {
		lp.program = string(cmdline[:j])
	} else {
		lp.program = string(cmdline)
	}
	environ, err := os.ReadFile(proc + "/environ")
	if err != nil {
		lp.state = procNotOurs
		return lp
	}
	for _, e := range strings.Split(string(environ), "\x00") {
		if v, ok := strings.CutPrefix(e, "CLAUDE_SSH_RUN_DIR="); ok {
			lp.runDir = v
		} else if v, ok := strings.CutPrefix(e, "CLAUDE_SSH_CHILD="); ok {
			lp.childMark = v
		}
	}
	return lp
}

// realReapWaitExit polls /proc until every pid's process is gone or timeout elapses, and
// returns the survivors. The poll returns as soon as all have exited, so the grace and
// escalate waits end early when the groups die quickly.
func realReapWaitExit(pids []int, timeout time.Duration) []int {
	deadline := reapNow().Add(timeout)
	survivors := append([]int(nil), pids...)
	for len(survivors) > 0 && reapNow().Before(deadline) {
		reapSleep(50 * time.Millisecond)
		var still []int
		for _, pid := range survivors {
			if readLiveProc(pid, false).state == procAlive {
				still = append(still, pid)
			}
		}
		survivors = still
	}
	return survivors
}
