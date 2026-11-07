package supervisor

import (
	"time"

	"github.com/schubydoo/claustrum/internal/logging"
)

// gitReadLimit is the time limit of the pane's git of a "gitRead":true spawn, from
// its start. No member of the request sets it. c9125eb8 sends the git SIGTERM 120 s
// after its start, and a git that ignores it runs about 7 s more (measured on Linux
// and macOS). Windows ends the git then (measured). A var so a test can shorten it.
var gitReadLimit = 120 * time.Second

// gitReadEndGrace is the wait after the SIGTERM of the limit. c9125eb8 sends the
// group SIGKILL 7 s after the SIGTERM to a git that still runs (measured on Linux
// and macOS). A var so a test can shorten it.
var gitReadEndGrace = 7 * time.Second

// watchGitReadLimit ends the pane's git p at limit, with the killedBy "limit".
// On Linux and macOS the SIGTERM goes to the git alone. A git that still runs after
// grace gets SIGKILL to its group. A git that ended on the SIGTERM gets no group
// SIGKILL, so a process in its group lives on (c9125eb8, measured on Linux and
// macOS). On Windows the TERM ends the git with exit code 1 (measured). That end is
// the kill of every other signal there (claustrum's own, see ProcGroup.Signal).
//
// Each signal goes through signalIfLive, so no signal reaches a reaped git. A git
// that ended before the limit, also one whose exit drain is still open, gets no line
// and no signal. Once the shutdown began, no limit end starts, as for the unattended
// end (claustrum's own, not measured).
func (m *Manager) watchGitReadLimit(p *Proc, limit, grace time.Duration) {
	limitTimer := time.NewTimer(limit)
	defer limitTimer.Stop()
	select {
	case <-p.done:
		return
	case <-limitTimer.C:
	}
	if !p.IsLive() || m.startsStopped.Load() {
		return
	}
	logging.Infof("[process.Manager] the pane's git has run for %v; ending it", limit)
	if !p.signalIfLive("TERM", "limit") {
		return
	}
	graceTimer := time.NewTimer(grace)
	defer graceTimer.Stop()
	select {
	case <-p.done:
		return
	case <-graceTimer.C:
	}
	p.signalIfLive("KILL", "limit")
}
