//go:build linux || darwin

package main

import (
	"strconv"
	"testing"
)

// TestOneBlank pins the form of the darwin start text: one blank between its words
// (macOS rows F1 to F4 and RP15c: 89cb6289 holds "Fri Oct 2 17:35:19 2026" where `ps`
// prints two blanks before the day). A text with a day of two digits has one blank
// already and stays as it is.
func TestOneBlank(t *testing.T) {
	for in, want := range map[string]string{
		"Thu Oct  1 15:39:22 2026":   "Thu Oct 1 15:39:22 2026",
		"Fri Oct  2 17:35:19 2026\n": "Fri Oct 2 17:35:19 2026",
		"Sun Sep 13 07:56:41 2026":   "Sun Sep 13 07:56:41 2026",
		"":                           "",
	} {
		if got := oneBlank(in); got != want {
			t.Errorf("oneBlank(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStartTextWithTwoBlanksDoesNotMatch pins the exact compare of the start text
// (macOS rows TBr, TBe and N6, 89cb6289 and claustrum). A record and a CLAUDE_SSH_CHILD entry of an
// older claustrum build hold the `ps` text with two blanks, and the live read of this
// build holds one blank. Such a record is not a child to end, its daemon reads as gone,
// and the leader test before a signal refuses it.
func TestStartTextWithTwoBlanksDoesNotMatch(t *testing.T) {
	const pid, daemon, runDir = 800000201, 800000202, "/run/c1"
	const two, one = "Thu Oct  1 15:39:22 2026", "Thu Oct 1 15:39:22 2026"
	fakeLiveProcs(t, map[int]liveProc{
		pid: {state: procAlive, startTicks: one, pgid: pid, program: "/f/bin/sigstub",
			runDir: runDir, childMark: strconv.Itoa(pid) + ":" + one},
		daemon: {state: procAlive, startTicks: one, pgid: daemon},
	})

	// The control: a record of this build verifies.
	rec := childRecord{Pid: pid, Start: one, Argv0: "/f/bin/sigstub"}
	if v, reason := verifyOrphan(rec, runDir, simOwnDaemon, simOwnParent, simOwnPgid); v != verdictReap {
		t.Fatalf("one-blank record: verdict %d (%q), want reap", v, reason)
	}
	old := childRecord{Pid: pid, Start: two, Argv0: "/f/bin/sigstub"}
	if v, reason := verifyOrphan(old, runDir, simOwnDaemon, simOwnParent, simOwnPgid); v != verdictSkip || reason != "the pid was reused" {
		t.Errorf("two-blank record: verdict %d (%q), want the reuse skip", v, reason)
	}
	if daemonAlive(daemon, two, simOwnDaemon) {
		t.Error("a live daemon with a two-blank record reads as alive, want gone")
	}
	if !daemonAlive(daemon, one, simOwnDaemon) {
		t.Error("a live daemon with a one-blank record reads as gone")
	}
	if v := sameLeader(pid, two); v != verdictSkip {
		t.Errorf("sameLeader with a two-blank start = %d, want skip", v)
	}
}

// TestStartTextMarkerWithTwoBlanksDoesNotMatch is the same rule for the marker: a
// child that an older claustrum build started holds two blanks in CLAUDE_SSH_CHILD.
func TestStartTextMarkerWithTwoBlanksDoesNotMatch(t *testing.T) {
	const pid, runDir = 800000203, "/run/c1"
	const two, one = "Thu Oct  1 15:39:22 2026", "Thu Oct 1 15:39:22 2026"
	fakeLiveProcs(t, map[int]liveProc{
		pid: {state: procAlive, startTicks: one, pgid: pid, program: "/f/bin/sigstub",
			runDir: runDir, childMark: strconv.Itoa(pid) + ":" + two},
	})
	rec := childRecord{Pid: pid, Start: one, Argv0: "/f/bin/sigstub"}
	if v, reason := verifyOrphan(rec, runDir, simOwnDaemon, simOwnParent, simOwnPgid); v != verdictSkip || reason != "process was not started directly by a daemon" {
		t.Errorf("two-blank marker: verdict %d (%q), want the not-started-by-a-daemon skip", v, reason)
	}
}
