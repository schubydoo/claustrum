package supervisor

import (
	"errors"
	"syscall"
	"time"

	"github.com/schubydoo/claustrum/internal/logging"
)

// groupEndAfterReap is false: Linux ends the group before the reap of the leader.
const groupEndAfterReap = false

// The times of the end on Linux. 87375cc7 sends SIGTERM 200 ms after the exit of the
// leader, and SIGKILL at the latest 1.5 s after that (measured on Linux). The look for
// a shutdown every 20 ms is claustrum's own. Vars so a test can shorten them.
var (
	groupTermAfter = 200 * time.Millisecond
	groupKillBound = 1500 * time.Millisecond
	groupLookEvery = 20 * time.Millisecond
)

// settleLeftGroupHeld ends what an exited launcher left in its group, while the
// unreaped leader holds the group number. With its pipes at their end, the group gets
// SIGKILL at once. Otherwise, 200 ms after the exit, it gets SIGTERM, whatever the group
// holds, and the line after the SIGKILL. SIGKILL follows once the pipes are at their end, at the
// latest after groupKillBound (87375cc7, measured on Linux). The 200 ms count from the
// moment the exit is seen. It returns that moment, or the zero time if it saw no exit.
//
// It marks it reaped before the wait, so a request in this wait treats it as exited and
// claims no killedBy for it, as in the exit drain (claustrum's own, not measured).
// leaderHeld stays true until this returns, before the wait of the exit goroutine.
//
// A shutdown that starts in the wait gets the group SIGKILL at once, with no SIGTERM,
// before the reap (87375cc7, measured on Linux). With -keep-children it sends nothing,
// so a member is kept (claustrum's own). A leader that the sweep asked to end with
// SIGTERM gets this normal end (87375cc7, measured on Linux). Only a SIGKILL of the
// sweep to the group stops it.
func (p *Proc) settleLeftGroupHeld(drained <-chan struct{}) time.Time {
	if p.cmd == nil || p.cmd.Process == nil {
		return time.Time{}
	}
	pid := p.cmd.Process.Pid
	if !exitSeenUnreaped(pid) {
		return time.Time{}
	}
	seen := time.Now()
	if p.tempRootKilled.Load() || p.unheldKilled.Load() {
		p.groupKillClaimed.Store(true)
		return seen
	}
	if !groupMadeAtSpawn(pid) {
		return seen
	}
	p.mu.Lock()
	// Under p.mu the sweep either signalled this child already, or finds leaderHeld
	// (killHeldGroupAtStop). A group that the sweep killed gets no signal below.
	asked := p.endedByShutdown
	p.reaped = true
	p.leaderHeld = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.leaderHeld = false
		p.mu.Unlock()
	}()
	stopped := p.shutdownBegan
	if asked {
		stopped = p.sweepKilledGroup.Load
	}
	// A spent claim ends the wait and the SIGTERM. A refused kill under way ends neither. 87375cc7
	// then sends the SIGKILL of the end at its usual time (measured on Linux, a client KILL after
	// the SIGTERM). A refused kill under way before the SIGTERM is claustrum's own, not measured.
	stopNow := func() bool { return stopped() || p.groupKillClaimed.spent() }
	termed := !awaitPipesEnd(drained, groupTermAfter-time.Since(seen), stopNow) && !stopNow() &&
		signalCheckedGroup(pid, syscall.SIGTERM) == nil
	if termed {
		awaitPipesEnd(drained, groupKillBound, stopNow)
	}
	if stopped() {
		if asked {
			p.groupKillClaimed.Store(true)
		} else {
			p.killHeldGroupAtStop()
		}
		return seen
	}
	p.sendHeldEndKill(pid)
	// 87375cc7 logs the SIGTERM line after the SIGKILL of the end, and after its refusal
	// line. A shutdown in the wait logs none (measured on Linux). After a client KILL that
	// ended the wait, the line still goes (claustrum's own, not measured).
	if termed {
		logging.Infof("[process.Manager] Process %s exited and its output was not at its end 200ms later: its process group was sent SIGTERM ahead of the SIGKILL", LogID(p.id))
	}
	return seen
}

// sendHeldEndKill sends the one group SIGKILL of the end. It runs under p.mu, so a kill
// of another path that is under way ends first. A refused one has given the claim back
// by then. With -keep-children, a shutdown that began before the last test, below, gets
// no SIGKILL. One that begins after that test can get it (claustrum's own, not measured).
//
// A kill call that fails logs one line with its error. 87375cc7 logs it after a refused
// SIGKILL of the end, and none after a refused SIGKILL of a shutdown (measured on Linux,
// EPERM). The line after another error is claustrum's own, not measured. A number that
// groupNumberRefused refuses makes no kill call and logs nothing (claustrum's own).
func (p *Proc) sendHeldEndKill(pid int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.keepsGroup && p.shutdownBegan() {
		return
	}
	if !p.groupKillClaimed.takeFinal() {
		return
	}
	err := signalCheckedGroup(pid, syscall.SIGKILL)
	p.heldGroupKilled = err == nil
	if err != nil && !errors.Is(err, errGroupRefused) {
		logging.Infof("[process.Manager] the process group of pid %d, which has exited, was not sent SIGKILL: %v", pid, err)
	}
}

// awaitPipesEnd returns once the pipes are at their end, after bound, or once stop
// reports true. It reports whether the pipes are at their end. At the bound it reads
// drained again, because a select picks at random when both are ready.
func awaitPipesEnd(drained <-chan struct{}, bound time.Duration, stop func() bool) bool {
	limit := time.NewTimer(bound)
	defer limit.Stop()
	look := time.NewTicker(groupLookEvery)
	defer look.Stop()
	for !stop() {
		select {
		case <-drained:
			return true
		case <-limit.C:
			select {
			case <-drained:
				return true
			default:
				return false
			}
		case <-look.C:
		}
	}
	return false
}
