package git

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The kill at the deadline of the content check. 87375cc7 ends the running
// `cat-file --batch` and the commit, tree and file hash-object calls at the
// timeoutMs deadline, also while another git call of the check runs. A check
// with no deadline, or one that ends before it, ends none.
//
// On Linux it sends SIGKILL to each process, before their stdin closes, 2 or 3
// times, and reaps when the check is free (measured). On macOS it sends SIGKILL
// at the deadline and again 2 s later or at the end of the check (measured for
// cat-file, commit and tree). No macOS row had a file hash running at the deadline.
// On Windows all four are gone within one poll of the deadline (measured).
// No group signal comes on Linux or macOS (measured).
//
// Claustrum's own, not measured: exactly one repeat, a helper not yet tracked when
// the kills go out gets no kill, and Windows has no repeat and no held reap. A
// `cat-file --buffer --batch` read gets no kill. A
// helper that hangs after another close of its stdin gets the kill with stdin closed.

// helperRepeatAfter is the time from the kill to its repeat (87375cc7, measured on
// Linux and macOS). The end of the check sends the repeat earlier. Tests replace it.
var helperRepeatAfter = 2 * time.Second

// deadlineHelper is one running helper that the deadline kills. The gate closes
// after its exit and before its reap, so no signal reaches a reaped process. drop
// closes its pipes, or is nil.
type deadlineHelper struct {
	p    *os.Process
	gate sysproc.LeaderGate
	drop func()
}

// helperKills holds the helpers of one check that the deadline kills. firedCh
// closes at the kill, and freeCh at the end of the check.
type helperKills struct {
	mu       sync.Mutex
	fired    bool
	repeated bool
	ended    bool
	helpers  []*deadlineHelper
	repeat   *time.Timer
	firedCh  chan struct{}
	freeCh   chan struct{}
}

func newHelperKills() *helperKills {
	return &helperKills{firedCh: make(chan struct{}), freeCh: make(chan struct{})}
}

// deadlineHelperKill sends the kill through the handle of the process. Tests
// replace it.
var deadlineHelperKill = func(p *os.Process) error { return p.Kill() }

// track is the step of a helper between its start and its reap (gitStop). It
// returns after the exit of the helper. After a kill on Linux and macOS it also
// waits for the end of the check, so the reap comes then. A helper not yet tracked
// when the kills go out gets no kill. Its pipes still close, so no read of the
// check waits for it (claustrum's own, not measured).
func (k *helperKills) track(p *os.Process, drop func()) {
	h := &deadlineHelper{p: p, drop: drop}
	k.mu.Lock()
	if k.fired || k.ended {
		fired := k.fired
		k.mu.Unlock()
		if fired && drop != nil {
			drop()
		}
		return
	}
	k.helpers = append(k.helpers, h)
	k.mu.Unlock()
	seen := awaitHelperExit(p)
	if holdKilledHelpers && k.sent() {
		<-k.freeCh
	}
	h.gate.CloseAfterExit(p, seen)
}

// sendAll sends the kill to each tracked helper that is not reaped. The caller
// holds k.mu.
func (k *helperKills) sendAll() {
	for _, h := range k.helpers {
		h.gate.Do(func() { _ = deadlineHelperKill(h.p) })
	}
}

// fire sends the kill once to each tracked helper, and then closes their pipes, so
// no read of the check waits for a killed helper. A nil k sends nothing.
func (k *helperKills) fire() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fired || k.ended {
		return
	}
	k.fired = true
	close(k.firedCh)
	k.sendAll()
	for _, h := range k.helpers {
		if h.drop != nil {
			h.drop()
		}
	}
	if holdKilledHelpers {
		k.repeat = time.AfterFunc(helperRepeatAfter, func() {
			k.mu.Lock()
			defer k.mu.Unlock()
			k.sendRepeat()
		})
	}
}

// sendRepeat sends the one repeat of the kill. The caller holds k.mu.
func (k *helperKills) sendRepeat() {
	if !k.repeated {
		k.repeated = true
		k.sendAll()
	}
}

// release ends the watch at the end of the check. A repeat that is still due goes
// out first, while the gates are open. Then the held reaps run.
func (k *helperKills) release() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ended = true
	if k.fired && holdKilledHelpers {
		k.repeat.Stop()
		k.sendRepeat()
	}
	close(k.freeCh)
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
// fires also while another git call of the check runs. It sets c.kills only in a
// check with a deadline and with the limits of a request.
func (c *contentCheck) armDeadlineKill() (stop func()) {
	if c.deadline == nil || c.u.stop == nil {
		return func() {}
	}
	kills := newHelperKills()
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
		kills.release()
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
func (c *contentCheck) helperTrack() func(*os.Process, func()) {
	if c.kills == nil {
		return nil
	}
	return c.kills.track
}

// endLong closes the pipes of b and waits for the end of its git. A kill ends the
// wait, because the reap of a killed helper waits for the end of the check.
func (c *contentCheck) endLong(b *objectBatch) {
	b.drop()
	if c.kills == nil {
		<-b.done
		return
	}
	select {
	case <-b.done:
	case <-c.kills.firedCh:
	}
}

// endedByKill reports that err of a long-running hash-object comes after the kill
// of the deadline. The check then stops with no stop line (87375cc7, measured on Linux).
// A hash-object that fails by itself after the kill counts too, and its stop line is not written (claustrum's own, not measured).
func (c *contentCheck) endedByKill(err error) bool {
	return !errors.Is(err, errNoHashHelper) && c.kills.sent() && c.pastDeadline()
}
