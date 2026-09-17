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
	// Reference build 90fca6e6 widened the gone test: a process whose vsize is 0
	// has an address space already torn down and counts as gone, where before only
	// the Z and X state letters did. Pointer-class, read from that build rather
	// than measured, because the reap's 5-minute age gate makes staging it live
	// impractical.
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
