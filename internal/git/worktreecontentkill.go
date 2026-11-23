package git

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The kill at the deadline of the content check. 87375cc7 ends the running
// `cat-file --batch` and the commit, tree and file hash-object calls at the
// timeoutMs deadline, also while another git call of the check runs. A check
// with no deadline, or one that ends before it, ends none.
//
// On Linux and macOS it sends SIGKILL to each process, and to the three long-running
// helpers before their stdin closes (measured on Linux). The file hash gets one send. The others get a repeat 2 s
// later or at the end of the check, and cat-file gets a third send in some rows.
// A killed file hash whose stdout a child holds keeps the reply about 2 s, and a
// row with no file hash has no such wait (measured). On Windows all four are gone
// within one poll of the deadline, with no such wait, and no git runs at the reply
// (measured). No group signal comes on Linux or macOS (measured).
//
// Claustrum's own, not measured: on Windows the check waits for the exit of each
// killed helper before the rollback, at most killedExitBound. That wait counts a
// start still under way. Also exactly one repeat, and Windows has no repeat and no
// held reap. After the kills a commit, tree or file hash does not start. A helper
// whose start was under way, or a batch that starts then, gets its kill at once. A
// `cat-file --buffer --batch` read gets no kill. The wait of the file hash is the
// end of its stdout, at most gitDrainWait. No row has a killed file hash with no
// child on its stdout. A helper that hangs after another close of its stdin gets
// the kill with stdin closed.

// helperRepeatAfter is the time from the kill to its repeat (87375cc7, measured on
// Linux and macOS). The end of the check sends the repeat earlier. Tests replace it.
var helperRepeatAfter = 2 * time.Second

// killedExitBound is the longest wait at the end of the check for the exit of the
// killed helpers on Windows, so a stuck process does not hold the request. The
// longest time from the deadline to the reply of 87375cc7 in the big-file rows is
// 2.07 s, with its rollback (measured on Windows). The 3 s are claustrum's own.
const killedExitBound = 3 * time.Second

// awaitExitsAtEnd turns on that wait. No Linux or macOS row needs it. Tests
// replace it.
var awaitExitsAtEnd = !holdKilledHelpers

// deadlineHelper is one running helper that the deadline kills. The gate closes
// after its exit and before its reap, so no signal reaches a reaped process. drop
// closes its pipes, or is nil. once marks the file hash, which gets no repeat and
// whose reap does not wait for the end of the check. exited closes at its exit.
type deadlineHelper struct {
	p      *os.Process
	gate   sysproc.LeaderGate
	drop   func()
	once   bool
	exited chan struct{}
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
	// starting counts the helpers between reserve and their track or failed
	// start. startsDone closes when it goes back to 0, and is nil at 0.
	starting   int
	startsDone chan struct{}
}

func newHelperKills() *helperKills {
	return &helperKills{firedCh: make(chan struct{}), freeCh: make(chan struct{})}
}

// helperExitWait is awaitHelperExit. Tests replace it.
var helperExitWait = awaitHelperExit

// deadlineHelperKill sends the kill through the handle of the process. Tests
// replace it.
var deadlineHelperKill = func(p *os.Process) error { return p.Kill() }

// track is the step of a helper between its start and its reap (gitStop). It
// returns after the exit of the helper. After a kill on Linux and macOS it also
// waits for the end of the check, so the reap comes then. A helper that comes after
// the kills went out gets its kill here, through its gate, and then its drop
// (claustrum's own, not measured).
func (k *helperKills) track(p *os.Process, drop func()) {
	k.runTracked(&deadlineHelper{p: p, drop: drop, exited: make(chan struct{})}, nil)
}

// trackOnce is track for the file hash, which has no pipes to drop. unreserve ends
// its count of reserve, or is nil.
func (k *helperKills) trackOnce(p *os.Process, unreserve func()) {
	k.runTracked(&deadlineHelper{p: p, once: true, exited: make(chan struct{})}, unreserve)
}

// reserve counts a helper before its start, so the end of the check knows about a
// start still under way. The function that it returns clears the count once.
func (k *helperKills) reserve() (unreserve func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.starting == 0 {
		k.startsDone = make(chan struct{})
	}
	k.starting++
	return sync.OnceFunc(func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		if k.starting--; k.starting == 0 {
			close(k.startsDone)
			k.startsDone = nil
		}
	})
}

// refuseStart is the last test before the start of a hash-object. After the kills
// that git does not start, as after a deadline during its listing.
func (k *helperKills) refuseStart() error {
	if k.sent() {
		return errContentDeadline
	}
	return nil
}

// awaitKilledExits waits for each start still under way and for the exit of each
// helper that the kills reached, at most bound in all. With no kill it returns at
// once.
func (k *helperKills) awaitKilledExits(bound time.Duration) {
	if !k.sent() {
		return
	}
	limit := time.NewTimer(bound)
	defer limit.Stop()
	for {
		k.mu.Lock()
		starts := k.startsDone
		k.mu.Unlock()
		if starts == nil {
			break
		}
		select {
		case <-starts:
		case <-limit.C:
			return
		}
	}
	k.mu.Lock()
	helpers := slices.Clone(k.helpers)
	k.mu.Unlock()
	for _, h := range helpers {
		if h.exited == nil {
			continue
		}
		select {
		case <-h.exited:
		case <-limit.C:
			return
		}
	}
}

// runTracked is the step of track for the helper h. unreserve ends its count of
// reserve, or is nil. Only a seen exit marks h as gone, so a failed wait keeps the
// end of the check waiting up to its bound.
func (k *helperKills) runTracked(h *deadlineHelper, unreserve func()) {
	k.mu.Lock()
	if k.ended && !k.fired {
		k.mu.Unlock()
		if unreserve != nil {
			unreserve()
		}
		return
	}
	k.helpers = append(k.helpers, h)
	late := k.fired
	if late {
		// One send, with no repeat and no held reap (claustrum's own, not measured).
		h.once = true
		h.gate.Do(func() { _ = deadlineHelperKill(h.p) })
	}
	k.mu.Unlock()
	if unreserve != nil {
		unreserve()
	}
	if late && h.drop != nil {
		h.drop()
	}
	seen := helperExitWait(h.p)
	if seen {
		close(h.exited)
	}
	if holdKilledHelpers && !h.once && k.sent() {
		<-k.freeCh
	}
	h.gate.CloseAfterExit(h.p, seen)
}

// fileHashEnd is the end of the file hash call.
type fileHashEnd struct {
	out []byte
	err error
}

// awaitFileHash waits for the end of the file hash call. After the kill it waits at
// most grace more. ok is false when the call did not end by then.
func awaitFileHash(ended <-chan fileHashEnd, fired <-chan struct{}, grace time.Duration) (r fileHashEnd, ok bool) {
	select {
	case r = <-ended:
		return r, true
	case <-fired:
	}
	if grace <= 0 {
		return r, false
	}
	wait := time.NewTimer(grace)
	defer wait.Stop()
	select {
	case r = <-ended:
		return r, true
	case <-wait.C:
		return r, false
	}
}

// fileHashGrace is the wait after the kill for the end of the file hash. On Linux
// and macOS it is the wait for the stdout of a call that exited. Windows has none.
func fileHashGrace() time.Duration {
	if holdKilledHelpers {
		return gitDrainWait
	}
	return 0
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
	if k.repeated {
		return
	}
	k.repeated = true
	for _, h := range k.helpers {
		if !h.once {
			h.gate.Do(func() { _ = deadlineHelperKill(h.p) })
		}
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
		if awaitExitsAtEnd {
			kills.awaitKilledExits(killedExitBound)
		}
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
