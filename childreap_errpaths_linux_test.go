//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestRealReadLiveProcStatArms covers the two stat-level verdicts of the reap's process
// read. The verdict decides whether a recorded child is signalled, so "unreadable" must
// never read as "alive and ours".
func TestRealReadLiveProcStatArms(t *testing.T) {
	_, mk, _ := fakeProc(t)
	fields16 := strings.Repeat("0 ", 16)

	// A stat line too short to carry the start-time. Without the length check the mutant
	// indexes past the end of the field slice.
	mk(90, "stat", "90 (x) R 1 90 too short")
	if lp := realReadLiveProc(90, false); lp.state != procNotOurs {
		t.Errorf("short stat line: state = %d, want procNotOurs (%+v)", lp.state, lp)
	}

	// Without the environment, the read stops at the stat: the namespace link, cmdline and
	// environ are never consulted. None of them exists in this fixture, so a mutant that
	// dropped the early return reads the process as not ours.
	mk(91, "stat", "91 (x) R 1 91 "+fields16+"9000 x")
	lp := realReadLiveProc(91, false)
	if lp.state != procAlive {
		t.Errorf("stat-only read: state = %d, want procAlive (%+v)", lp.state, lp)
	}
	if lp.pgid != 91 || lp.startTicks != "9000" {
		t.Errorf("stat-only read: pgid=%d startTicks=%q, want 91 and \"9000\"", lp.pgid, lp.startTicks)
	}
}

// TestKillGroupAndGroupAliveRealBodies exercises the two destructive seams' own
// implementations, which every other test in this package replaces. Signal 0 delivers
// nothing and only reports whether the group can be signalled, so this is safe to run
// against our own process group: it is the same technique the reap's liveness probe uses.
func TestKillGroupAndGroupAliveRealBodies(t *testing.T) {
	self := syscall.Getpgrp()
	if err := killGroup(self, 0); err != nil {
		t.Errorf("killGroup(own group, 0) = %v, want nil", err)
	}
	if !groupAlive(self) {
		t.Error("groupAlive(own group) = false, want true")
	}

	// A group that cannot exist: a pid above pid_max. Without this arm, a killGroup of
	// `return nil` and a groupAlive of `return true` both pass the assertions above, and
	// unlike the reaped-pid arm below this one is race-free.
	const impossible = 1 << 30
	if err := killGroup(impossible, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("killGroup(impossible group, 0) = %v, want ESRCH", err)
	}
	if groupAlive(impossible) {
		t.Error("groupAlive(impossible group) = true, want false")
	}

	// The same answers for a group id that existed and was reaped, which is the shape the
	// reap actually meets. The kernel could hand that number to a new process between the
	// reap and the call, so a different answer skips rather than fails; the arm above is
	// what keeps the test an oracle if that happens.
	exe, env := helperCommand(t, "exit:0")
	cmd := exec.Command(exe)
	cmd.Env = buildEnv(env)
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	dead := cmd.Process.Pid
	if _, err := os.Stat("/proc/" + strconv.Itoa(dead)); err == nil {
		t.Skipf("pid %d was reused before the assertion", dead)
	}
	if err := killGroup(dead, 0); !errors.Is(err, syscall.ESRCH) {
		t.Skipf("killGroup(reaped group %d, 0) = %v, not ESRCH: the pid was reused mid-test", dead, err)
	}
	if groupAlive(dead) {
		t.Errorf("groupAlive(dead group %d) = true, want false", dead)
	}
}
