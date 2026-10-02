//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
)

// The linux-specific half of the orphan reap: it reads a live process from /proc and
// gathers this daemon's node/host identity from the linux sources. The shared decision
// logic (reapOrphans, verifyOrphan, the two-phase wait) lives in childreap.go.

// procRoot is the /proc mount realReadLiveProc reads. A var so a test can point it at a
// fake procfs tree and exercise the parse-error arms.
var procRoot = "/proc"

// procReadFile reads one file of a /proc/<pid> folder. A var so a test can let the
// process end between two reads of one realReadLiveProc call.
var procReadFile = os.ReadFile

// ownReapIdentity returns this daemon's node and host for the reap's record-matching gate,
// from the same linux sources childrecord writes: node = <boot_id>/pid:[<pidns-inode>],
// host = "machine-id:<id>".
func ownReapIdentity() (node, host string) {
	return nodeID(), "machine-id:" + machineID()
}

// realReadLiveProc reads /proc/<pid> for the reap. It always reads stat (state, group id,
// and start-time). When wantEnv is set it also applies the pid-namespace guard and reads
// the program from cmdline and the two markers from environ.
func realReadLiveProc(pid int, wantEnv bool) liveProc {
	lp := liveProc{state: procGone}
	proc := procRoot + "/" + strconv.Itoa(pid)
	stat, err := procReadFile(proc + "/stat")
	if err != nil {
		return lp // gone
	}
	i := strings.LastIndexByte(string(stat), ')')
	if i < 0 {
		lp.state = procNotOurs
		return lp
	}
	fields := strings.Fields(string(stat)[i+1:])
	// A process whose vsize is 0 has an address space already torn down, so it
	// counts as gone beside the Z and X state letters. That rule is claustrum's own
	// and is not probe-measured, because the reap's 5-minute age gate makes staging
	// it live impractical.
	//
	// claustrum therefore reads vsize (stat field 23) alongside the state letter,
	// which is why the floor below is 21 fields rather than 20.
	if len(fields) <= 20 {
		lp.state = procNotOurs
		return lp
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return lp // zombie or dead: gone
	}
	if fields[20] == "0" { // vsize
		return lp
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
	cmdline, err := procReadFile(proc + "/cmdline")
	if err != nil {
		lp.state = endedOr(pid, procNotOurs)
		return lp
	}
	if len(cmdline) == 0 {
		lp.state = procGone
		return lp
	}
	// cmdline holds each argument with a NUL after it, so an argument stays whole,
	// spaces included. The first one is the program and the rest are the arguments.
	argv := strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00")
	lp.program, lp.args = argv[0], argv[1:]
	environ, err := procReadFile(proc + "/environ")
	if err != nil {
		if lp.state = endedOr(pid, procNotOurs); lp.state == procGone {
			return lp
		}
		// The text is the reference's reason for this case (89cb6289, Linux row PG05a).
		lp.unreadable = "environment unreadable: " + err.Error()
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

// endedOr answers a read of cmdline or environ that failed. The stat read came first
// and showed a live process, so the process can have ended in between: a reaped
// process has no /proc folder any more, and a zombie refuses the read of its environ.
// A new read of the stat tells the two cases apart. It returns procGone when the
// process has ended, and state when it is still there and really is unreadable.
//
// Measured on Linux (rows K5 and ED04a, 2 of 52 cells): a child that exited on the
// SIGTERM was read as not ours, and the reap logged it as a group that no longer
// verifies. 89cb6289 counts such a group as ended on SIGTERM.
func endedOr(pid, state int) int {
	if realReadLiveProc(pid, false).state == procGone {
		return procGone
	}
	return state
}
