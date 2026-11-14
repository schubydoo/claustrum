package supervisor

import (
	"time"

	"github.com/schubydoo/claustrum/internal/logging"
)

// tempRootEndGrace is the wait from the SIGTERM of a child whose private temp folder
// is lost to the SIGKILL of its group. 4c111e86 sends the SIGKILL about 3 s after the
// SIGTERM to a child that ignores it (measured on macOS). A var so a test can shorten
// it.
var tempRootEndGrace = 3 * time.Second

// endForLostTempRoot ends p after the temp-folder step of package main found its
// private temp folder lost, with the killedBy "temp_root". The step calls it only
// for a child whose spawn asked with "cliTempRootEnd":true. 4c111e86 logs the line,
// sends the SIGTERM, and sends the SIGKILL to a child that still runs after the wait
// (measured on Linux and macOS, the SIGKILL and the line order on macOS). A
// process.kill in the wait keeps the killedBy "temp_root" (measured on macOS).
//
// Each signal goes through signalIfLiveLocked, so no signal reaches a reaped child,
// and the SIGKILL goes to the group as in watchGitReadLimit. A child that ends on the
// SIGTERM gets a group SIGKILL before its reap (killGroupOnTempRootExit). Once the
// shutdown began, no end starts (claustrum's own, not measured).
func (m *Manager) endForLostTempRoot(p *Proc, grace time.Duration) {
	if !p.IsLive() || m.startsStopped.Load() {
		return
	}
	// The id is cut (4c111e86, measured on Linux and macOS).
	logging.Infof("[process.Manager] Process %s is being ended: its private temp folder is lost", LogID(p.id))
	// The SIGTERM and its flag go under one hold of p.mu. The flag, not killedBy,
	// tells the exit path that this end signalled: a process.kill before it keeps
	// the killedBy "client".
	p.mu.Lock()
	live, err := p.signalIfLiveLocked("TERM", "temp_root")
	p.tempRootTermed = p.tempRootTermed || (live && err == nil)
	p.mu.Unlock()
	if !live {
		return
	}
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-p.done:
		return
	case <-t.C:
	}
	tempRootBeforeKill(p)
	// The SIGKILL and the flag go under one hold of p.mu, so the exit path never takes
	// a pending SIGKILL for a sent one. A group SIGKILL of the exit path sets the flag
	// too, so the group gets one SIGKILL.
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.tempRootKilled {
		live, _ := p.signalIfLiveLocked("KILL", "temp_root")
		p.tempRootKilled = live
	}
}

// tempRootBeforeKill runs after the wait of endForLostTempRoot, before its SIGKILL. A
// seam for the tests. Production never reassigns it.
var tempRootBeforeKill = func(*Proc) {}

// killGroupOnTempRootExit runs in the exit goroutine of a child whose spawn asked with
// cliTempRootEnd, before the reap. Once the child has exited, while it is not reaped
// yet, it sends SIGKILL to the group of a child that the temp_root end sent SIGTERM and
// that did not get the SIGKILL of that end. The child that is not reaped holds its
// pid, so the group id cannot be reused. The group must be the one that the spawn
// made: its id is the pid of the child. This is claustrum's own rule (decision-099).
// Where the exit cannot be seen without a reap, it sends nothing.
func (p *Proc) killGroupOnTempRootExit() {
	if p.cmd == nil || p.cmd.Process == nil || !exitSeenUnreaped(p.cmd.Process.Pid) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.tempRootTermed || p.tempRootKilled || !groupMadeAtSpawn(p.cmd.Process.Pid) {
		return
	}
	_ = signalGroup(p.group, p.cmd.Process, "KILL")
	p.tempRootKilled = true
}
