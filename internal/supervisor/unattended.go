package supervisor

import (
	"errors"
	"syscall"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// A process.spawn with a top-level unattendedSeconds above 0 is armed (87375cc7,
// measured on Linux, macOS and Windows). The daemon ends such a child once no
// connection has been attached to it for its time.
// processSpawn refuses the value with pty, linkBound, gitRead and cliTempRootEnd, so
// an armed child has none of them.
//
// The rules, measured on Linux, macOS and Windows unless marked:
//
//   - The clock is the time since the last connection went. An attached connection
//     stops it, and a reattach clears it. A client that dies counts as gone at once.
//   - A check runs each step of unattendedTick, counted from the spawn. The end comes
//     at the first step with no connection for the whole time.
//   - The end, Linux and macOS (measured there): SIGTERM to the child only, and a wait until it
//     leaves, at most 7 s. Then SIGKILL goes to its group, also after the child left.
//   - The end, Windows (measured there): the SIGTERM ends the job, and the child leaves.
//   - A SIGTERM refused with EPERM, Linux and macOS (measured there): one line at the
//     first refusal, and no SIGKILL. Each later step with no connection for the whole
//     time sends a new SIGTERM. One that goes through runs the usual end (measured
//     for a child that stays on Linux and one that leaves on macOS).
//   - A child that a process.kill ended, an own exit and server.shutdown give no end
//     and no line. A child that survives a process.kill still gets the end (claustrum's
//     own, not measured).

// UnheldTTL maps an accepted unattendedSeconds n above 0 to the time a child lives
// with no connection. That is 60 s up to 60, then n s, and 168 h from 604800 on.
// 87375cc7, measured on Linux, macOS and Windows for 1, 60, 61, 604799 and 604800.
func UnheldTTL(n int64) time.Duration {
	switch {
	case n <= 60:
		return time.Minute
	case n >= 604800:
		return 168 * time.Hour
	default:
		return time.Duration(n) * time.Second
	}
}

// unheldEndGrace is the most time from the SIGTERM of the end to the group SIGKILL.
// 87375cc7 sends that SIGKILL 7002 ms after the SIGTERM to a child that ignores it
// (measured on Linux and macOS). var so a test can shrink it. Production never reassigns it.
var unheldEndGrace = 7 * time.Second

// unheldTicker gives the steps of the check, and the stop of the steps. A seam, so a
// test drives the check with times of its own. Production never reassigns it.
var unheldTicker = func(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// unheldBeforeEnd runs in the check after a step found the whole time, right before
// endUnheld. A seam for a test that changes the state there. Production never
// reassigns it.
var unheldBeforeEnd = func(*Proc) {}

// unheldExitSeen runs in the exit goroutine of an armed child once its exit is seen,
// before the gate closes and before the reap. A seam for a test that steps the check
// there. Production never reassigns it.
var unheldExitSeen = func(*Proc) {}

// unheldHooks holds unheldTicker, unheldEndGrace, unheldBeforeEnd and unheldExitSeen as
// spawnWith read them, so a check that outlives its test reads none of them.
type unheldHooks struct {
	ticker    func(time.Duration) (<-chan time.Time, func())
	grace     time.Duration
	beforeEnd func(*Proc)
	exitSeen  func(*Proc)
}

// readUnheldHooks reads the four values of unheldHooks.
func readUnheldHooks() unheldHooks {
	return unheldHooks{ticker: unheldTicker, grace: unheldEndGrace, beforeEnd: unheldBeforeEnd, exitSeen: unheldExitSeen}
}

// markUnheldLocked starts the clock of an armed p once when unheld is true, and
// clears it when unheld is false. The caller holds p.mu.
func (p *Proc) markUnheldLocked(now time.Time, unheld bool) {
	switch {
	case p.unheldTTL == 0:
	case !unheld:
		p.unheldSince = time.Time{}
	case p.unheldSince.IsZero():
		p.unheldSince = now
	}
}

// dropSubLocked takes c off the subscribers of p. Once none is left, the clock of an
// armed p starts. The caller holds p.mu.
func (p *Proc) dropSubLocked(c *conn.Conn, now time.Time) {
	if _, ok := p.subs[c]; !ok {
		return
	}
	delete(p.subs, c)
	if len(p.subs) == 0 {
		p.markUnheldLocked(now, true)
	}
}

// unheldFor is how long p has had no connection, or 0 while one is attached.
func (p *Proc) unheldFor(now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unheldSince.IsZero() {
		return 0
	}
	return now.Sub(p.unheldSince)
}

// watchUnheld runs the check of an armed p until its exit, and starts the end at the
// first step with no connection for p.unheldTTL. The steps count from the spawn, a
// quarter of the time (87375cc7, measured). The cap of 60 s, and a step from the mapped
// time for a value from 2 to 59 (on Windows also 1), are claustrum's own, not measured.
// A step short of the whole time skips the end. endUnheld tests the shutdown and the
// clock again under p.mu, so a shutdown or a reattach before the SIGTERM ends nothing.
func (m *Manager) watchUnheld(p *Proc, h unheldHooks) {
	ticks, stop := h.ticker(unattendedTick(p.unheldTTL))
	defer stop()
	for {
		var now time.Time
		select {
		case <-p.done:
			return
		case now = <-ticks:
			if p.unheldFor(now) < p.unheldTTL {
				continue
			}
		}
		h.beforeEnd(p)
		if !m.endUnheld(p, now, h.grace) {
			return
		}
	}
}

// endUnheld sends SIGTERM to the child of p only, through signalIfLiveLocked, and
// waits for its exit, at most grace. A child that is still there then gets the group
// SIGKILL. Both signals go through unheldGate. On Linux and macOS it closes once the
// exit of the child is seen, before its reap. Before that, the SIGTERM goes only to a
// child that sysproc.SurelyRuns finds running, so no signal of the end reaches an
// exited child there. On Windows it closes after cmd.Wait returns. A child there that
// exits by itself just before that can still get killedBy "unattended" and the left
// line: a terminate call on an exited child counts as a delivery. No signal leaves
// the handle or the job of that child (claustrum's own, not measured). A closed gate
// sends nothing and logs nothing. The group SIGKILL of a child that left comes from
// killGroupOnUnheldExit.
//
// Under p.mu, right before the SIGTERM, the end tests the shutdown and the clock at
// now again. After the shutdown began it sends nothing. After a reattach it sends
// nothing and reports true, so the check goes on. After a SIGTERM refused with EPERM
// it reports true too, and it logs the refused line once for each child. Only Linux
// and macOS make such an error. Another failed SIGTERM stops the end with no line and
// no SIGKILL, and that child gets no later end (claustrum's own, not measured).
func (m *Manager) endUnheld(p *Proc, now time.Time, grace time.Duration) (again bool) {
	termed, firstRefusal := false, false
	p.unheldGate.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.stopsBegan.Load() {
			return
		}
		if p.unheldSince.IsZero() || now.Sub(p.unheldSince) < p.unheldTTL {
			again = true
			return
		}
		// A child that left by itself gets no end, as for the temp_root takeover.
		if exitSeenBeforeReap && p.cmd != nil && p.cmd.Process != nil && !sysproc.SurelyRuns(p.cmd.Process) {
			return
		}
		live, err := p.signalIfLiveLocked("TERM", "unattended")
		p.unheldTermed = live && err == nil
		termed = p.unheldTermed
		if live && errors.Is(err, syscall.EPERM) {
			again = true
			firstRefusal = !p.unheldRefusalLogged
			p.unheldRefusalLogged = true
		}
	})
	if firstRefusal {
		logging.Infof("[process.Manager] Process %s: no connection attached for %s, but it could not be sent SIGTERM", LogID(p.id), p.unheldTTL)
	}
	if !termed {
		return again
	}
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-p.done:
		return false
	case <-t.C:
	}
	p.unheldGate.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.unheldKilled.Load() || p.stopsBegan.Load() {
			return
		}
		live, _ := p.signalIfLiveLocked("KILL", "unattended")
		p.unheldKilled.Store(live)
		p.unheldStayed = live
	})
	return false
}

// killGroupOnUnheldExit runs in the exit goroutine of an armed child, before the
// reap. It waits for the exit of a child that is not reaped yet. Then it sends
// SIGKILL to the group of a child that endUnheld sent SIGTERM and did not kill. 87375cc7 sends
// it 1.4 to 52 ms after such a child left (measured on Linux and macOS). The group must be the
// one that the spawn made, as in killGroupOnTempRootExit. Then it closes unheldGate.
// Where the exit cannot be seen without a reap (Windows), it sends nothing and leaves
// the gate open: the exit goroutine closes it after the reap, and there the end
// signals the child by its handle. Once the shutdown began, it sends nothing
// (claustrum's own).
func (p *Proc) killGroupOnUnheldExit() {
	if p.cmd == nil || p.cmd.Process == nil {
		p.unheldGate.Close()
		return
	}
	if !exitSeenBeforeReap {
		return
	}
	seen := exitSeenUnreaped(p.cmd.Process.Pid)
	if seen {
		p.unheldGate.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if !p.unheldTermed || p.unheldKilled.Load() || p.stopsBegan.Load() || !groupMadeAtSpawn(p.cmd.Process.Pid) {
				return
			}
			_ = signalGroup(p.group, p.cmd.Process, "KILL")
			p.unheldKilled.Store(true)
		})
	}
	if seen && p.unheldExitSeen != nil {
		p.unheldExitSeen(p)
	}
	p.unheldGate.CloseAfterExit(p.cmd.Process, seen)
}

// logUnheldEnd logs the line of the end of an armed p after its reap, before its
// exited line, once endUnheld sent the SIGTERM. The texts are the reference's
// (87375cc7, measured on Linux and macOS, and the first on Windows). The cut of a long id is
// claustrum's own, not measured.
func (p *Proc) logUnheldEnd() {
	p.mu.Lock()
	termed, stayed := p.unheldTermed, p.unheldStayed
	p.mu.Unlock()
	switch {
	case !termed:
	case stayed:
		logging.Infof("[process.Manager] Process %s: no connection attached for %s: it stayed at SIGTERM, and its group was killed", LogID(p.id), p.unheldTTL)
	default:
		logging.Infof("[process.Manager] Process %s: no connection attached for %s: it left at SIGTERM", LogID(p.id), p.unheldTTL)
	}
}
