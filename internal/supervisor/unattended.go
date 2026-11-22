package supervisor

import (
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/logging"
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
//   - The end, Linux and macOS (measured there): SIGTERM to the child alone, and a wait until it
//     leaves, at most 7 s. Then SIGKILL goes to its group, also after the child left.
//   - The end, Windows (measured there): the SIGTERM ends the job, and the child leaves.
//   - process.kill, an own exit and server.shutdown give no end and no line.

// AloneTTL maps an accepted unattendedSeconds n above 0 to the time a child lives
// with no connection. That is 60 s up to 60, then n s, and 168 h from 604800 on.
// 87375cc7, measured on Linux, macOS and Windows for 1, 60, 61, 604799 and 604800.
func AloneTTL(n int64) time.Duration {
	switch {
	case n <= 60:
		return time.Minute
	case n >= 604800:
		return 168 * time.Hour
	default:
		return time.Duration(n) * time.Second
	}
}

// aloneEndGrace is the most time from the SIGTERM of the end to the group SIGKILL.
// 87375cc7 sends that SIGKILL 7002 ms after the SIGTERM to a child that ignores it
// (measured on Linux and macOS). var so a test can shrink it. Production never reassigns it.
var aloneEndGrace = 7 * time.Second

// aloneTicker gives the steps of the check, and the stop of the steps. A seam, so a
// test drives the check with times of its own. Production never reassigns it.
var aloneTicker = func(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// aloneBeforeEnd runs in the check after a step found the whole time, right before
// endAlone. A seam for a test that changes the state there. Production never
// reassigns it.
var aloneBeforeEnd = func(*Proc) {}

// aloneHooks holds aloneTicker, aloneEndGrace and aloneBeforeEnd as spawnWith read
// them, so a check that outlives its test reads none of the three.
type aloneHooks struct {
	ticker    func(time.Duration) (<-chan time.Time, func())
	grace     time.Duration
	beforeEnd func(*Proc)
}

// readAloneHooks reads the three values of aloneHooks.
func readAloneHooks() aloneHooks {
	return aloneHooks{ticker: aloneTicker, grace: aloneEndGrace, beforeEnd: aloneBeforeEnd}
}

// markAloneLocked starts the clock of an armed p once when alone is true, and
// clears it when alone is false. The caller holds p.mu.
func (p *Proc) markAloneLocked(now time.Time, alone bool) {
	switch {
	case p.aloneTTL == 0:
	case !alone:
		p.aloneSince = time.Time{}
	case p.aloneSince.IsZero():
		p.aloneSince = now
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
		p.markAloneLocked(now, true)
	}
}

// aloneFor is how long p has had no connection, or 0 while one is attached.
func (p *Proc) aloneFor(now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.aloneSince.IsZero() {
		return 0
	}
	return now.Sub(p.aloneSince)
}

// watchAlone runs the check of an armed p until its exit, and starts the end at the
// first step with no connection for p.aloneTTL. A step below 60 s, from a value
// below 60, is claustrum's own, not measured: the step comes from the mapped time.
// A step short of the whole time skips the end. endAlone tests the shutdown and the
// clock again under p.mu, so a shutdown or a reattach before the SIGTERM ends nothing.
func (m *Manager) watchAlone(p *Proc, h aloneHooks) {
	ticks, stop := h.ticker(unattendedTick(p.aloneTTL))
	defer stop()
	for {
		var now time.Time
		select {
		case <-p.done:
			return
		case now = <-ticks:
			if p.aloneFor(now) < p.aloneTTL {
				continue
			}
		}
		h.beforeEnd(p)
		if !m.endAlone(p, now, h.grace) {
			return
		}
	}
}

// endAlone sends SIGTERM to the child of p alone, through signalIfLiveLocked, and
// waits for its exit, at most grace. A child that is still there then gets the group
// SIGKILL. Both signals go through aloneGate. On Linux and macOS it closes once the
// exit of the child is seen, before its reap, so no signal of the end reaches an
// exited child there. On Windows it closes after cmd.Wait returns. A child there that
// exits by itself just before that can still get killedBy "unattended" and the left
// line: a TerminateProcess on an exited child counts as a delivery. No signal leaves
// the handle or the job of that child (claustrum's own, not measured). A closed gate
// sends nothing and logs nothing. The group SIGKILL of a child that left comes from
// killGroupOnAloneExit.
//
// Under p.mu, right before the SIGTERM, the end tests the shutdown and the clock at
// now again. After the shutdown began it sends nothing. After a reattach it sends
// nothing and reports true, so the check goes on. A refused SIGTERM stops the end with
// no line and no SIGKILL, and the child gets no later end (claustrum's own, not
// measured).
func (m *Manager) endAlone(p *Proc, now time.Time, grace time.Duration) (again bool) {
	termed := false
	p.aloneGate.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.stopsBegan.Load() {
			return
		}
		if p.aloneSince.IsZero() || now.Sub(p.aloneSince) < p.aloneTTL {
			again = true
			return
		}
		live, err := p.signalIfLiveLocked("TERM", "unattended")
		p.aloneTermed = live && err == nil
		termed = p.aloneTermed
	})
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
	p.aloneGate.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.aloneKilled || p.stopsBegan.Load() {
			return
		}
		live, _ := p.signalIfLiveLocked("KILL", "unattended")
		p.aloneKilled = live
		p.aloneStayed = live
	})
	return false
}

// killGroupOnAloneExit runs in the exit goroutine of an armed child, before the
// reap. It waits for the exit of a child that is not reaped yet. Then it sends
// SIGKILL to the group of a child that endAlone sent SIGTERM and did not kill. 87375cc7 sends
// it 1.4 to 52 ms after such a child left (measured on Linux and macOS). The group must be the
// one that the spawn made, as in killGroupOnTempRootExit. Then it closes aloneGate.
// Where the exit cannot be seen without a reap (Windows), it sends nothing and leaves
// the gate open: the exit goroutine closes it after the reap, and there the end
// signals the child by its handle. Once the shutdown began, it sends nothing
// (claustrum's own).
func (p *Proc) killGroupOnAloneExit() {
	if p.cmd == nil || p.cmd.Process == nil {
		p.aloneGate.Close()
		return
	}
	if !exitSeenBeforeReap {
		return
	}
	seen := exitSeenUnreaped(p.cmd.Process.Pid)
	if seen {
		p.aloneGate.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if !p.aloneTermed || p.aloneKilled || p.stopsBegan.Load() || !groupMadeAtSpawn(p.cmd.Process.Pid) {
				return
			}
			_ = signalGroup(p.group, p.cmd.Process, "KILL")
			p.aloneKilled = true
		})
	}
	p.aloneGate.CloseAfterExit(p.cmd.Process, seen)
}

// logAloneEnd logs the line of the end of an armed p after its reap, before its
// exited line, once endAlone sent the SIGTERM. The texts are the reference's
// (87375cc7, measured on Linux and macOS, and the first on Windows). The cut of a long id is
// claustrum's own, not measured.
func (p *Proc) logAloneEnd() {
	p.mu.Lock()
	termed, stayed := p.aloneTermed, p.aloneStayed
	p.mu.Unlock()
	switch {
	case !termed:
	case stayed:
		logging.Infof("[process.Manager] Process %s: no connection attached for %s: it stayed at SIGTERM, and its group was killed", LogID(p.id), p.aloneTTL)
	default:
		logging.Infof("[process.Manager] Process %s: no connection attached for %s: it left at SIGTERM", LogID(p.id), p.aloneTTL)
	}
}
