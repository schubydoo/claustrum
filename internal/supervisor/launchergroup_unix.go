//go:build linux || darwin

package supervisor

import (
	"errors"
	"os"
	"syscall"
)

// LauncherGroupEndBuilt is true: an exited launcher's group ends on Linux and macOS.
const LauncherGroupEndBuilt = true

// errGroupRefused is the answer for a group number that gets no signal.
var errGroupRefused = errors.New("refused a signal to this process group")

// signalLeftGroup is the one kill call of this end. A seam for the tests.
var signalLeftGroup = func(pgid int, sig syscall.Signal) error { return syscall.Kill(-pgid, sig) }

// groupNumberRefused reports a number that never names a launcher's own group: 1 or
// below, this daemon's pid, or this daemon's group (claustrum's own, not measured).
func groupNumberRefused(pgid, ownPid, ownGroup int) bool {
	return pgid <= 1 || pgid == ownPid || pgid == ownGroup
}

// signalCheckedGroup sends sig to the group after groupNumberRefused.
func signalCheckedGroup(pgid int, sig syscall.Signal) error {
	if groupNumberRefused(pgid, os.Getpid(), syscall.Getpgrp()) {
		return errGroupRefused
	}
	return signalLeftGroup(pgid, sig)
}

// probeThenKillGroup is the macOS end: kill(-pgid, 0), then SIGKILL after an answer of
// 0 (87375cc7, measured on macOS). After an error it sends none (claustrum's own).
func probeThenKillGroup(pgid int) {
	if signalCheckedGroup(pgid, 0) != nil {
		return
	}
	_ = signalCheckedGroup(pgid, syscall.SIGKILL)
}

// shutdownBegan reports that the shutdown of the daemon began.
func (p *Proc) shutdownBegan() bool { return p.stopsBegan != nil && p.stopsBegan.Load() }

// groupAlreadyEnded is the test of the macOS end. The sweep, the temp-root end or the
// unattended end already killed this group, or a shutdown runs with -keep-children.
// This end then sends nothing (claustrum's own, not measured). A leader that exits
// after the shutdown began still gets the end (87375cc7, measured on macOS). It reads
// atomics and takes no lock, so nothing waits between the reap and the kill.
func (p *Proc) groupAlreadyEnded() bool {
	return (p.keepsGroup && p.shutdownBegan()) || p.sweepKilledGroup.Load() ||
		p.tempRootKilled.Load() || p.unheldKilled.Load()
}

// killHeldGroupAtStop sends the group SIGKILL of a shutdown, once, while the end on
// Linux holds the unreaped leader. leaderHeld clears under p.mu before the wait, so
// this signal never comes after the reap. 87375cc7 kills the group at the shutdown and
// counts it as ended (measured on Linux). It reports whether a shutdown killed this
// group. With -keep-children it sends nothing (claustrum's own). macOS never holds a
// leader.
func (p *Proc) killHeldGroupAtStop() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.leaderHeld && !p.keepsGroup && !p.groupKillClaimed.Swap(true) {
		p.stopKilledGroup = signalCheckedGroup(p.cmd.Process.Pid, syscall.SIGKILL) == nil
	}
	return p.stopKilledGroup
}

// settleLeftGroupReaped runs on macOS right after the reap of a launcher. The group
// number can then name an unrelated group if the kernel reused it. The maintainer
// accepted that risk to follow 87375cc7 (AGENTS.md).
func (p *Proc) settleLeftGroupReaped() {
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	p.leaderReapSeen.Store(true)
	if p.groupAlreadyEnded() {
		p.groupKillClaimed.Store(true)
		return
	}
	// One atomic claim, so no lock and no wait comes between the reap and the kill.
	if p.groupKillClaimed.Swap(true) {
		return
	}
	probeThenKillGroup(p.cmd.Process.Pid)
}
