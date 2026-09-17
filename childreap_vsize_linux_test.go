//go:build linux

package main

import (
	"strconv"
	"strings"
	"testing"
)

// Reference build 90fca6e6 widened the "this process is gone" test in the reap's
// /proc read. Before, the state letter alone decided it: Z or X meant gone. Now a
// vsize of 0 counts as gone too, because a process whose address space is already
// torn down reports one, and the state letter alone reads it as alive.
//
// Pointer-class: read from that build rather than measured. The reap's 5-minute
// age gate makes staging it live impractical.
//
// claustrum reads vsize from stat field 23, so its own field-count floor rises
// with it: a line that stops earlier cannot answer the question.

// statLine builds a /proc/<pid>/stat line with the given state and vsize. The field
// order after the closing paren is state, ppid, pgrp, then 16 more, then starttime
// (index 19) and vsize (index 20).
func statLine(pid int, state, vsize string) string {
	return "1 (x) " + state + " 1 " + strconv.Itoa(pid) + " " +
		strings.Repeat("0 ", 16) + "9000 " + vsize
}

// TestRealReadLiveProcVsizeZeroIsGone pins the new gone test. A state-R process with
// a vsize of 0 has no address space left, so the reference reads it as gone and
// claustrum must too.
func TestRealReadLiveProcVsizeZeroIsGone(t *testing.T) {
	_, mk, _ := fakeProc(t)

	// The control that must keep passing: a live process with a real vsize.
	mk(70, "stat", statLine(70, "R", "4194304"))
	if lp := realReadLiveProc(70, false); lp.state != procAlive {
		t.Fatalf("live process with a real vsize: state = %d, want procAlive", lp.state)
	}

	// The new arm. Without the fix claustrum answers procAlive here, and the reap
	// then treats a process the reference calls gone as a live orphan.
	mk(71, "stat", statLine(71, "R", "0"))
	if lp := realReadLiveProc(71, false); lp.state != procGone {
		t.Errorf("state R with vsize 0: state = %d, want procGone", lp.state)
	}

	// A zombie keeps its existing verdict, whatever its vsize says. This arm fails a
	// mutant that replaced the state test with the vsize test instead of adding it.
	mk(72, "stat", statLine(72, "Z", "4194304"))
	if lp := realReadLiveProc(72, false); lp.state != procGone {
		t.Errorf("state Z with a real vsize: state = %d, want procGone", lp.state)
	}
}

// TestRealReadLiveProcNeedsVsizeField pins the field-count floor. A line that stops
// at starttime cannot answer the vsize question, so it must not read as alive.
func TestRealReadLiveProcNeedsVsizeField(t *testing.T) {
	_, mk, _ := fakeProc(t)

	// 20 fields after the paren: state, ppid, pgrp, 16 zeros, starttime. Index 20 is
	// absent. claustrum's floor admits this today and answers procAlive.
	mk(73, "stat", "1 (x) R 1 73 "+strings.Repeat("0 ", 16)+"9000")
	if lp := realReadLiveProc(73, false); lp.state != procNotOurs {
		t.Errorf("stat line without a vsize field: state = %d, want procNotOurs", lp.state)
	}

	// 21 fields is the first line the new read can judge.
	mk(74, "stat", statLine(74, "R", "4194304"))
	if lp := realReadLiveProc(74, false); lp.state != procAlive {
		t.Errorf("stat line with a vsize field: state = %d, want procAlive", lp.state)
	}
}
