package main

import (
	"time"
)

// A link-bound process (process.spawn with "linkBound", 683cc6a9) belongs to the
// connection of its spawn, its link. Measured on Linux:
//
//   - Its stream frames go to the link only.
//   - process.stdin from another connection answers "Process not found".
//   - Once the link is gone, the process ends after its unattended time.
//   - At its exit the record goes, and its id stays spent for a while.
//
// macOS and Windows are not measured.

// linkBoundParams is the linkBound member of process.spawn. A missing
// unattendedSeconds counts as 0.
type linkBoundParams struct {
	UnattendedSeconds int64 `json:"unattendedSeconds"`
}

// unattendedTTL maps unattendedSeconds to the time a link-bound process lives with
// no link. Below 1 it is 60 s, from 1 to 599 it is n s, and 600 or more is cut to
// 600 s (683cc6a9, measured on Linux for 0, 1, 2, 4, 120, 599, 600 and 100000).
// An absent or negative value counts as 0 (claustrum's own, not measured).
func unattendedTTL(n int64) time.Duration {
	switch {
	case n < 1:
		return time.Minute
	case n >= 600:
		return 10 * time.Minute
	default:
		return time.Duration(n) * time.Second
	}
}

// unattendedTick is the step of the unattended check: a quarter of the time, at
// most 60 s. The ticker starts at the spawn. That is the smallest rule that fits
// the measured end times on Linux: about 1 s after the time for 1 and 4, about
// 15 s after for 60, 29 s after for 120 and 60 s after for 599 and 600. The rule
// itself is claustrum's own, not measured.
func unattendedTick(ttl time.Duration) time.Duration {
	return min(ttl/4, time.Minute)
}

// unattendedEndGrace is the wait between the SIGHUP of the unattended end and the
// SIGKILL. 683cc6a9 logs the SIGKILL exit about 7 s after its ending line (measured
// on Linux). var so a test can shrink it. Production never reassigns it.
var unattendedEndGrace = 7 * time.Second

// linkSpentAge is how long the id of a link-bound process stays spent after its
// exit. 683cc6a9 refuses a spawn of that id 1.5 s after the exit and starts it
// 931.8 s after (measured on Linux). 900 s is claustrum's own, not measured. Copied
// into the manager at construction. var so tests can shrink it.
var linkSpentAge = 15 * time.Minute

// linkReplayCap is the replay cap of a link-bound process. It fits the one Linux
// row: 200000 bytes in 7 frames left frames 6 and 7 in the replay. The byte count
// is claustrum's own, not measured.
const linkReplayCap int64 = 64 * 1024

// errSpentID is the refusal of a spawn whose id is spent (683cc6a9, measured on
// Linux). The command does not run. A spawn while a link-bound process of that id
// still runs or starts gets it too (claustrum's own, not measured).
var errSpentID = &spawnRefusal{codeInvalidParam, "this id is in use, or was a terminal's or a link-bound process's: take a fresh one"}

// takesFrom reports whether c may write the stdin of p. A plain process takes it
// from every connection, a link-bound one from its link only.
func (p *managedProc) takesFrom(c *conn) bool {
	return !p.linked || p.link == c
}

// idTakenLocked reports whether a spawn of id is refused: the id is spent, or a
// link-bound process of that id is in the table. The caller holds m.mu.
func (m *procManager) idTakenLocked(id string) bool {
	if _, ok := m.spent[id]; ok {
		return true
	}
	if _, ok := m.linkStarting[id]; ok {
		return true
	}
	p := m.procs[id]
	return p != nil && p.linked
}

// pruneSpentLocked drops the spent ids older than spentAge. The caller holds m.mu.
func (m *procManager) pruneSpentLocked(now time.Time) {
	for id, at := range m.spent {
		if now.Sub(at) >= m.spentAge {
			delete(m.spent, id)
		}
	}
}

// forgetLinked removes the record of a link-bound process at its exit and spends
// its id. The record of another process with the same id stays.
func (m *procManager) forgetLinked(p *managedProc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.procs[p.id] == p {
		delete(m.procs, p.id)
	}
	m.spent[p.id] = time.Now()
}

// markDetachedLocked starts the unattended clock of p once. The caller holds p.mu.
func (p *managedProc) markDetachedLocked(now time.Time) {
	if p.linked && p.detachedAt.IsZero() {
		p.detachedAt = now
	}
}

// connGone reports whether c is closed. A nil c counts as gone.
func connGone(c *conn) bool {
	if c == nil {
		return true
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.closed
}

// unattendedFor is how long p has had no link, or 0 while its link is there.
func (p *managedProc) unattendedFor(now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.detachedAt.IsZero() {
		return 0
	}
	return now.Sub(p.detachedAt)
}

// watchUnattended ends p once it has had no link for ttl, checked each tick. The
// end is the kill path of process.killAndWait with the label unattended. The
// ending line is followed by SIGHUP, and a child that is still there gets SIGKILL
// about 7 s later (683cc6a9, measured on Linux). claustrum sends the SIGHUP to the
// child alone and the SIGKILL to its group, as for a client kill (claustrum's own).
// On Windows the first signal already ends the child (claustrum's own, not
// measured). No frame goes out, because no link is attached. Once the shutdown
// began, no new unattended end starts: with -keep-children the drop of the
// connections starts each clock, and a kept child gets no unattended signal
// (claustrum's own).
func (m *procManager) watchUnattended(p *managedProc, ttl, tick, grace time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case now := <-t.C:
			if p.unattendedFor(now) < ttl {
				continue
			}
		}
		if m.startsStopped.Load() {
			return
		}
		logInfof("[process.Manager] Process %s: no connection attached for %s; ending it", p.id, ttl)
		m.killAndWaitAs(p, "HUP", "unattended", grace, true)
		return
	}
}
