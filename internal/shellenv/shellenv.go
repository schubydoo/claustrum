package shellenv

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/schubydoo/claustrum/internal/logging"
)

// The daemon reads the PATH of the login shell for its children. Measured on Linux
// and macOS against 52d82f0a:
//
//   - The read starts at the start of the daemon, with no spawn. The value 1 of
//     CLAUDE_SSH_NO_LOGIN_PATH_WARM turns it off, and then the first spawn starts it.
//     The value 0 does not. The daemon removes the variable from its own environment,
//     so no child inherits it from the daemon.
//   - A spawn waits for a running read at most 4 s, and never past 8 s after the start
//     of the read. A child that comes before a good read gets the daemon's own PATH.
//   - A failed read is tried again by a later spawn, 30 s after the first failure, then
//     after 1m0s and 2m0s. A good read is kept. Only the read again below runs after it.
//   - A spawn with "disableShellAgentSocket":true ends a read that still runs after its
//     wait. After a failure such a spawn starts no other read.
//   - The shutdown ends a running read. On Linux the read logs nothing then.
//
// The PATH read and the SSH agent hand-off share one gate (loginShellGate). While one
// login shell runs, the other step runs none.
//
// claustrum's own, not measured: the doubling of the pause after 2m0s, the limit of 5
// failed reads for the spawn wait, the cap of 16 failed reads, the wait of 1 s after
// the stop, and that only a spawn starts a try again.
//
// After a good read, a good server.adoptEnv lets the PATH be read again
// (NoteAdoptedPATH, RefreshLoginPATH). Measured on Linux and macOS against c9125eb8:
//
//   - The adopt starts no read. The next spawn of any connection starts it in the
//     background, also a spawn on a connection that never adopted. That spawn keeps
//     the old PATH, and later spawns of every connection get the new one.
//   - An adopt with the same PATH as before lets a read start too.
//   - A spawn with "disableShellAgentSocket":true starts none. The next plain spawn
//     does.
//   - An adopt with "noLoginShell":true starts none.
//   - A spawn 28 s to 31 s after the start of the last read starts none, and one
//     61 s after it does.
//   - On Linux, the login shell of the read gets the PATH of the adopt.
//   - On Linux, a failed read logs the failure line, and the spawns keep the old
//     PATH. The next good read logs "after 1 failed read(s)".
//   - On Linux, after 24 reads again, the line of the cap follows the good line of
//     the 24th read (loginPATHAgainLine).
//
// claustrum's own, not measured:
//
//   - the floor of exactly 60 s from the start of the last read
//   - that only good reads count toward the cap, in a window of 24 h from the start
//     of the first read, and a new window from the first read again that starts
//     after it ends
//   - that a failed read again lets a later spawn try again after its pause
//   - that a good read clears the failure count
//   - that a gitRead spawn starts no read again
//   - that a taken gate starts none and keeps the adopt pending
//   - that a spawn inside the floor keeps the adopt pending
//   - that an adopt before the first good read counts too
//   - that the shell gets the daemon PATH when the adopt sent none
//   - that a read again holds the gate, so a spawn whose SSH agent step falls due
//     meanwhile runs no shell for it
//
// On Windows no login shell runs. The read takes the daemon's own PATH
// (shellenv_windows.go). claustrum's own there, not measured: the read at the start
// and the removal of the variable.

// NoLoginPATHWarmEnv names the variable whose value 1 keeps the read from starting
// at the start of the daemon.
const NoLoginPATHWarmEnv = "CLAUDE_SSH_NO_LOGIN_PATH_WARM"

// The bounds of the read and of the spawn wait. Variables so tests can shrink them.
var (
	// loginPATHSpawnWait is the longest wait of one spawn for a running read.
	loginPATHSpawnWait = 4 * time.Second
	// loginPATHSpawnHorizon is the age of a read after which no spawn waits for it.
	loginPATHSpawnHorizon = 8 * time.Second
	// loginPATHFirstPause is the pause after the first failure. Each later failure
	// doubles it.
	loginPATHFirstPause = 30 * time.Second
	// loginPATHStopWait is the longest wait for a read after its stop (not measured).
	loginPATHStopWait = 1 * time.Second
	// loginPATHRefreshAfter is the age of the last read from which a spawn after a
	// good adopt reads again. Measured on c9125eb8: none at 31 s, one at 61 s. The
	// exact 60 s is claustrum's own, not measured.
	loginPATHRefreshAfter = 60 * time.Second
	// loginPATHAgainWindow is the window of the cap of the reads again (claustrum's
	// own, not measured: the line of the cap names a time 24 h after its start).
	loginPATHAgainWindow = 24 * time.Hour
)

// loginPATHAgainCap is the count of good reads again in one window after which no
// read again starts (measured on Linux against c9125eb8: 24 reads, then the line).
const loginPATHAgainCap = 24

// loginPATHAgainLine is the line of the cap (measured on Linux against c9125eb8).
const loginPATHAgainLine = "[shellenv] Since %s the login shell's PATH has been read again %d times, each time because a new connection sent its environment. It is not read again before %s; until then programs keep the PATH just read"

const (
	// loginPATHWaitFailures is the count of failed reads from which no spawn waits
	// for a read (claustrum's own, not measured).
	loginPATHWaitFailures = 5
	// loginPATHMaxFailures is the count of failed reads after which no read runs
	// again (claustrum's own, not measured).
	loginPATHMaxFailures = 16
)

// LoginPATHExtractor runs one read. The context ends at a stop. Production uses the
// per-platform extractLoginPATH. A seam for tests.
var LoginPATHExtractor = extractLoginPATH

// loginShellGate lets one login shell run at a time: the PATH read or the run of the
// SSH agent hand-off. A step that finds it taken runs no shell.
var loginShellGate = make(chan struct{}, 1)

// tryGate takes the gate g, or reports false at once when another run holds it.
func tryGate(g chan struct{}) bool {
	select {
	case g <- struct{}{}:
		return true
	default:
		return false
	}
}

// loginPATHRun is one read that runs.
type loginPATHRun struct {
	started time.Time
	cancel  context.CancelFunc
	end     chan struct{} // closed when the read has its result
	gate    chan struct{} // the gate that this read holds
	stopped bool          // a spawn or the shutdown asked for the stop
	quiet   bool          // the shutdown stopped it, so it logs nothing
	again   bool          // a read after a good read (RefreshLoginPATH)
}

// loginPATHState is the state of the PATH read of the daemon.
type loginPATHState struct {
	mu       sync.Mutex
	armed    bool // only the daemon arms the read (runServe of package main), never a test server of package main
	closed   bool // the daemon shuts down, so no read starts
	good     bool // a read gave a PATH, so no read runs again
	path     string
	failures int
	nextTry  time.Time // the earliest start of the next read after a failure
	run      *loginPATHRun
	lastRead time.Time // the start of the last read

	// The read again after a good adopt.
	adoptPending bool      // a good adopt came since the last read again started
	adoptPATH    string    // the PATH of that adopt, or "" for the daemon PATH
	againSince   time.Time // the start of the window of the cap
	againCount   int       // the good reads again in that window

	// Seams for tests.
	clock func() time.Time
	gate  chan struct{}
}

var loginPATHs = &loginPATHState{clock: time.Now, gate: loginShellGate}

// CurrentLoginPATH returns the PATH of the last good read, or "" before one.
func CurrentLoginPATH() string {
	loginPATHs.mu.Lock()
	defer loginPATHs.mu.Unlock()
	return loginPATHs.path
}

// armLoginPATH lets the reads of the login-shell PATH run. A test server of package
// main never arms them, so it never starts a login shell.
func armLoginPATH() {
	loginPATHs.mu.Lock()
	loginPATHs.armed = true
	loginPATHs.mu.Unlock()
}

// WarmAtStart arms the read and starts it, unless the variable of
// NoLoginPATHWarmEnv is 1. Either way the variable leaves the daemon environment.
func WarmAtStart() {
	warm := os.Getenv(NoLoginPATHWarmEnv) != "1"
	_ = os.Unsetenv(NoLoginPATHWarmEnv)
	armLoginPATH()
	if warm {
		loginPATHs.mu.Lock()
		loginPATHs.startLocked()
		loginPATHs.mu.Unlock()
	}
}

// AwaitLoginPATH runs at each spawn before its environment is built. It starts a
// read that is due and waits for a running read, within the bounds above. A spawn
// with noShellAgent ends a read that still runs after that wait.
func AwaitLoginPATH(noShellAgent bool) {
	s := loginPATHs
	s.mu.Lock()
	if !s.armed || s.good || s.closed {
		s.mu.Unlock()
		return
	}
	if s.run == nil && (!s.dueLocked(noShellAgent) || !s.startLocked()) {
		s.mu.Unlock()
		return
	}
	r, failures, age := s.run, s.failures, s.clock().Sub(s.run.started)
	s.mu.Unlock()
	if failures < loginPATHWaitFailures {
		if wait := min(loginPATHSpawnWait, loginPATHSpawnHorizon-age); wait > 0 {
			waitUpTo(r.end, wait)
		}
	}
	if noShellAgent {
		s.stop(r, false)
	}
}

// dueLocked reports whether a spawn starts a new read.
func (s *loginPATHState) dueLocked(noShellAgent bool) bool {
	if s.failures == 0 {
		return true
	}
	if s.failures >= loginPATHMaxFailures || noShellAgent {
		return false
	}
	return !s.clock().Before(s.nextTry)
}

// startLocked starts a read in the background. It starts none when a read runs,
// after a good read, at the shutdown, and when the gate is taken. A taken gate
// counts no failure (claustrum's own, not measured).
func (s *loginPATHState) startLocked() bool {
	if s.good {
		return false
	}
	return s.launchLocked(false, "")
}

// launchLocked starts a read in the background, unless a read runs, the daemon
// shuts down or the gate is taken. again marks a read after a good read, and path
// is the PATH of its shell.
func (s *loginPATHState) launchLocked(again bool, path string) bool {
	if s.run != nil || s.closed || !tryGate(s.gate) {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &loginPATHRun{started: s.clock(), cancel: cancel, end: make(chan struct{}), gate: s.gate, again: again}
	s.run, s.lastRead = r, r.started
	if s.againSince.IsZero() {
		s.againSince = r.started
	}
	go s.read(withShellPATH(ctx, path), r)
	return true
}

// NoteAdoptedPATH runs at a good server.adoptEnv with no "noLoginShell":true. path
// is the PATH that the adopt sent, or "" when it sent none. The next spawn that
// RefreshLoginPATH lets through reads the PATH again with it. Where no login shell
// runs it does nothing.
func NoteAdoptedPATH(path string) {
	if !loginPATHFromShell {
		return
	}
	s := loginPATHs
	s.mu.Lock()
	s.adoptPending, s.adoptPATH = true, path
	s.mu.Unlock()
}

// RefreshLoginPATH runs at a spawn with no "disableShellAgentSocket":true and no
// gitRead, after the SSH agent step. After a good read and a good adopt, once the
// last read started at least loginPATHRefreshAfter ago, it starts one read again in
// the background and does not wait. It starts none after loginPATHAgainCap good
// reads again in the window, or before the pause of a failure ends. Where no login
// shell runs it does nothing.
func RefreshLoginPATH() {
	if !loginPATHFromShell {
		return
	}
	s := loginPATHs
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	if !s.armed || !s.good || !s.adoptPending || now.Sub(s.lastRead) < loginPATHRefreshAfter {
		return
	}
	if s.failures >= loginPATHMaxFailures || now.Before(s.nextTry) {
		return
	}
	// A new window starts only with a read that launches. No read runs then, so no
	// read of the old window counts in the new one (claustrum's own, not measured).
	newWindow := !now.Before(s.againSince.Add(loginPATHAgainWindow))
	if !newWindow && s.againCount >= loginPATHAgainCap {
		return
	}
	if s.launchLocked(true, s.adoptPATH) {
		s.adoptPending = false
		if newWindow {
			s.againSince, s.againCount = now, 0
		}
	}
}

// read runs one read, records its result and logs it. The gate goes free when the
// read returns (claustrum's own, not measured).
func (s *loginPATHState) read(ctx context.Context, r *loginPATHRun) {
	path, err := s.extract(ctx)
	<-r.gate
	r.cancel()
	s.mu.Lock()
	s.run = nil
	took := s.clock().Sub(r.started).Round(100 * time.Millisecond)
	switch {
	case r.quiet:
	case err == nil:
		s.good, s.path = true, path
		if loginPATHFromShell {
			logging.Debugf("[shellenv] Extracted shell PATH (%d chars) in %s, after %d failed read(s)", len(path), took, s.failures)
		}
		s.failures = 0
		if r.again {
			s.againCount++
			// A read that ends after its window ends logs no line, because the next
			// read again opens a new window (claustrum's own, not measured).
			if s.againCount == loginPATHAgainCap && s.clock().Before(s.againSince.Add(loginPATHAgainWindow)) {
				// The level of this line is claustrum's own (not measured).
				logging.Infof(loginPATHAgainLine, rfc3339UTC(s.againSince), s.againCount, rfc3339UTC(s.againSince.Add(loginPATHAgainWindow)))
			}
		}
	default:
		s.failures++
		if r.again {
			// A later spawn tries again after the pause (claustrum's own, not measured).
			s.adoptPending = true
		}
		logging.Warnf("[shellenv] Could not read PATH from the login shell after %s (%s): %v", took, s.failureNoteLocked(), err)
	}
	s.mu.Unlock()
	close(r.end)
}

// extract runs the extractor. A panic in it fails the read and does not end the
// daemon, as a panic of a request does not (claustrum's own).
func (s *loginPATHState) extract(ctx context.Context) (path string, err error) {
	defer func() {
		if v := recover(); v != nil {
			logging.Errorf("[shellenv] recovered panic in the read of the login-shell PATH: %v", v)
			path, err = "", fmt.Errorf("recovered panic: %v", v)
		}
	}()
	return LoginPATHExtractor(ctx)
}

// failureNoteLocked records when the next read can start, and renders the note of
// the failure line. After a good read the note of c9125eb8 follows (measured on
// Linux for the first failure of a read again). Before one the older note stays
// (claustrum's own for c9125eb8, not measured).
func (s *loginPATHState) failureNoteLocked() string {
	if s.failures >= loginPATHMaxFailures {
		// The text of this note is claustrum's own (not measured).
		if s.good {
			return fmt.Sprintf("failure %d; no further try; programs keep the last PATH that was read", s.failures)
		}
		return fmt.Sprintf("failure %d; no further try; programs get the daemon's own PATH", s.failures)
	}
	pause := loginPATHFirstPause << (s.failures - 1)
	s.nextTry = s.clock().Add(pause)
	keep := "programs get the daemon's own PATH"
	if s.good {
		keep = "programs keep the last PATH that was read, or sshd's if none was"
	}
	return fmt.Sprintf("failure %d; next try in %s at the earliest; until one works, %s", s.failures, pause, keep)
}

// rfc3339UTC renders t in UTC to the second, as the line of the cap does.
func rfc3339UTC(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// shellPATHKey is the context key of the PATH for the shell of a read.
type shellPATHKey struct{}

// withShellPATH gives the shell of the read of ctx the PATH path. An empty path
// keeps the daemon PATH.
func withShellPATH(ctx context.Context, path string) context.Context {
	if path == "" {
		return ctx
	}
	return context.WithValue(ctx, shellPATHKey{}, path)
}

// stop ends the read r if it still runs, and waits for its end at most
// loginPATHStopWait. A stop of the shutdown logs nothing, and the read logs nothing.
// The cancel of the context kills the process group of the shell.
func (s *loginPATHState) stop(r *loginPATHRun, shutdown bool) {
	s.mu.Lock()
	if s.run != r {
		s.mu.Unlock()
		return
	}
	if shutdown {
		r.quiet = true
	} else if !r.stopped {
		logging.Infof("[shellenv] Stopping the read of the login shell's PATH at the desktop's word " +
			"(disableShellAgentSocket); spawns that carry it start no other after a failure")
	}
	r.stopped = true
	s.mu.Unlock()
	r.cancel()
	waitUpTo(r.end, loginPATHStopWait)
}

// Stop ends a running read at the shutdown. No read starts after it. It
// changes nothing when the read is not armed, as in a test server of package main.
func Stop() {
	s := loginPATHs
	s.mu.Lock()
	if !s.armed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	r := s.run
	s.mu.Unlock()
	if r != nil {
		s.stop(r, true)
	}
}

// waitUpTo waits until ch is closed or d has passed.
func waitUpTo(ch <-chan struct{}, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	}
}
