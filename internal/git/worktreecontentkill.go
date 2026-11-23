package git

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The kill at the deadline of the content check. On Linux, 4c111e86 and
// 87375cc7 send one SIGKILL to the process of the running `cat-file --batch`
// and of the running commit hash-object, 0 to 2 ms after the timeoutMs deadline
// (measured on Linux). No SIGTERM and no group signal come. The kill comes
// before the close of their stdin, and also while another git call of the check
// runs. A check with no deadline, or one that ends before it, sends none.
//
// Those builds also kill the tree hash-object and the hash-object of the files
// (measured on Linux). Claustrum does not, so these two run as before. macOS and
// Windows send no signal here, because no row measures them.
//
// Claustrum's own, not measured: a helper that starts after the deadline gets no
// kill, and one kill goes to each helper. 87375cc7 sends a second SIGKILL 2 s
// later, to a helper that is dead and not yet reaped (measured on Linux).

// deadlineHelper is one running helper that the deadline kills. The gate closes
// after its exit and before its reap, so no signal reaches a reaped process.
type deadlineHelper struct {
	p    *os.Process
	gate sysproc.LeaderGate
}

// helperKills holds the helpers of one check that the deadline kills.
type helperKills struct {
	mu      sync.Mutex
	fired   bool
	helpers []*deadlineHelper
}

// deadlineHelperKill sends the SIGKILL through the handle of the process. Tests
// replace it.
var deadlineHelperKill = func(p *os.Process) error { return p.Kill() }

// track is the step of a helper between its start and its reap (gitStop). It
// returns after the exit of the helper. A helper that starts after the kills
// went out is not tracked.
func (k *helperKills) track(p *os.Process) {
	h := &deadlineHelper{p: p}
	k.mu.Lock()
	if k.fired {
		k.mu.Unlock()
		return
	}
	k.helpers = append(k.helpers, h)
	k.mu.Unlock()
	h.gate.CloseAfterExit(p, sysproc.AwaitExit(p.Pid))
}

// fire sends the kill once to each tracked helper that is not reaped. A nil k
// sends nothing.
func (k *helperKills) fire() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fired {
		return
	}
	k.fired = true
	for _, h := range k.helpers {
		h.gate.Do(func() { _ = deadlineHelperKill(h.p) })
	}
}

// sent reports that the deadline kills went out.
func (k *helperKills) sent() bool {
	if k == nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.fired
}

// armDeadlineKill starts the watch of the deadline, and returns its stop. It
// fires also while another git call of the check runs. It sets c.kills only on
// Linux, in a check with a deadline and with the limits of a request.
func (c *contentCheck) armDeadlineKill() (stop func()) {
	if !helperKillAtDeadline || c.deadline == nil || c.u.stop == nil {
		return func() {}
	}
	kills := &helperKills{}
	c.kills = kills
	ctx, keep := c.deadline, c.keepTook
	done := make(chan struct{})
	cancel := context.AfterFunc(ctx, func() {
		if callerTimeoutFired(ctx) && waitForKeep(ctx, keep, done) {
			kills.fire()
		}
	})
	return func() {
		cancel()
		close(done)
	}
}

// waitForKeep waits past the deadline of ctx for the time of the LFS keep, which
// does not count (keepWithinDeadline). It returns false when done closes first.
func waitForKeep(ctx context.Context, keep time.Duration, done <-chan struct{}) bool {
	dl, ok := ctx.Deadline()
	if !ok || keep <= 0 {
		return true
	}
	wait := time.Until(dl.Add(keep))
	if wait <= 0 {
		return true
	}
	select {
	case <-time.After(wait):
		return true
	case <-done:
		return false
	}
}

// helperTrack is the step that a killed helper runs after its start, or nil.
func (c *contentCheck) helperTrack() func(*os.Process) {
	if c.kills == nil {
		return nil
	}
	return c.kills.track
}

// endedByKill reports that err of a long-running hash-object follows the kill
// of the deadline. The check then stops with no stop line (87375cc7, measured
// on Linux).
func (c *contentCheck) endedByKill(err error) bool {
	return !errors.Is(err, errNoHashHelper) && c.kills.sent() && c.pastDeadline()
}
