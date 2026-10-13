//go:build linux

package main

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
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

// procAbsent reports whether a read error of /proc means that the process does not
// exist. Every other error means that the process cannot be inspected.
func procAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

// unparseableStat is the reference's reason for a stat that does not parse
// (`5fd08069`, cells H2d and H4, Linux VM).
const unparseableStat = "unparseable stat"

// pidNamespace compares the pid namespace link of a child with the daemon's own. A
// link of the child that does not exist gives procGone, which is claustrum's own
// choice. `5fd08069` removes the record there and sends no signal, with no line (cell
// H3, Linux VM). A link that cannot be read gives procNotChild. `5fd08069` drops the
// record there with this reason and sends no signal, for "permission denied" (cell
// H3v, Linux VM). claustrum does so for every other read error too (not measured).
// Another namespace gives procNotOurs, as before. With no own link there is no test,
// as before (not measured).
func pidNamespace(proc string) (state int, why string) {
	self, err := os.Readlink(procRoot + "/self/ns/pid")
	if err != nil {
		return procAlive, ""
	}
	other, err := os.Readlink(proc + "/ns/pid")
	switch {
	case err == nil && other == self:
		return procAlive, ""
	case err == nil:
		return procNotOurs, ""
	case procAbsent(err):
		return procGone, ""
	}
	return procNotChild, "pid namespace unreadable: " + err.Error()
}

// realReadLiveProc reads /proc/<pid> for the reap. It always reads stat (state, group id,
// and start-time) and the pid namespace link. When wantEnv is set it applies the
// pid-namespace guard to the state and reads the program from cmdline and the two
// markers from environ. Without wantEnv the result of the guard goes into nsState.
//
// Only a stat that does not exist is a process that is gone. A stat that cannot be
// read or does not parse gives procUnknown. `5fd08069` keeps the record and sends no
// signal for such a stat of the daemon (cells H2a to H2c, Linux VM) and for a group
// field of the child that is not an integer (cell H2d). The state and the reason text
// of a child stat that cannot be read are claustrum's own, not measured.
func realReadLiveProc(pid int, wantEnv bool) liveProc {
	lp := liveProc{state: procGone}
	proc := procRoot + "/" + strconv.Itoa(pid)
	stat, err := procReadFile(proc + "/stat")
	if err != nil {
		if !procAbsent(err) {
			lp.state, lp.why = procUnknown, "stat unreadable: "+err.Error()
		}
		return lp
	}
	i := strings.LastIndexByte(string(stat), ')')
	if i < 0 {
		lp.state, lp.why = procUnknown, unparseableStat
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
		lp.state, lp.why = procUnknown, unparseableStat
		return lp
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return lp // zombie or dead: gone
	}
	if fields[20] == "0" { // vsize
		return lp
	}
	// A group field that is not an integer does not parse (cell H2d).
	if lp.pgid, err = strconv.Atoi(fields[2]); err != nil { // field 5 (pgrp)
		lp.state, lp.why = procUnknown, unparseableStat
		return lp
	}
	lp.startTicks = fields[19] // field 22 (starttime)
	lp.state = procAlive
	ns, why := pidNamespace(proc)
	if !wantEnv {
		lp.nsState, lp.why = ns, why
		return lp
	}
	// The pid-namespace guard: a process in another namespace is not ours to judge.
	if ns != procAlive {
		lp.state, lp.why = ns, why
		return lp
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
