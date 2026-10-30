package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
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
//     after 1m0s and 2m0s. A good read is kept, and no read runs after it.
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
// On Windows no login shell runs. The read takes the daemon's own PATH
// (shellenv_windows.go). claustrum's own there, not measured: the read at the start
// and the removal of the variable.

// noLoginPATHWarmEnv names the variable whose value 1 keeps the read from starting
// at the start of the daemon.
const noLoginPATHWarmEnv = "CLAUDE_SSH_NO_LOGIN_PATH_WARM"

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
)

const (
	// loginPATHWaitFailures is the count of failed reads from which no spawn waits
	// for a read (claustrum's own, not measured).
	loginPATHWaitFailures = 5
	// loginPATHMaxFailures is the count of failed reads after which no read runs
	// again (claustrum's own, not measured).
	loginPATHMaxFailures = 16
)

// loginPATHExtractor runs one read. The context ends at a stop. Production uses the
// per-platform extractLoginPATH. A seam for tests.
var loginPATHExtractor = extractLoginPATH

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
}

// loginPATHState is the state of the PATH read of the daemon.
type loginPATHState struct {
	mu       sync.Mutex
	armed    bool // only the daemon arms the read (runServe), never a test server
	closed   bool // the daemon shuts down, so no read starts
	good     bool // a read gave a PATH, so no read runs again
	path     string
	failures int
	nextTry  time.Time // the earliest start of the next read after a failure
	run      *loginPATHRun

	// Seams for tests.
	clock func() time.Time
	gate  chan struct{}
}

var loginPATHs = &loginPATHState{clock: time.Now, gate: loginShellGate}

// currentLoginPATH returns the PATH of the last good read, or "" before one.
func currentLoginPATH() string {
	loginPATHs.mu.Lock()
	defer loginPATHs.mu.Unlock()
	return loginPATHs.path
}

// armLoginPATH lets the reads of the login-shell PATH run. A test server never arms
// them, so it never starts a login shell.
func armLoginPATH() {
	loginPATHs.mu.Lock()
	loginPATHs.armed = true
	loginPATHs.mu.Unlock()
}

// warmLoginPATHAtStart arms the read and starts it, unless the variable of
// noLoginPATHWarmEnv is 1. Either way the variable leaves the daemon environment.
func warmLoginPATHAtStart() {
	warm := os.Getenv(noLoginPATHWarmEnv) != "1"
	_ = os.Unsetenv(noLoginPATHWarmEnv)
	armLoginPATH()
	if warm {
		loginPATHs.mu.Lock()
		loginPATHs.startLocked()
		loginPATHs.mu.Unlock()
	}
}

// awaitLoginPATH runs at each spawn before its environment is built. It starts a
// read that is due and waits for a running read, within the bounds above. A spawn
// with noShellAgent ends a read that still runs after that wait.
func awaitLoginPATH(noShellAgent bool) {
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
	if s.run != nil || s.good || s.closed || !tryGate(s.gate) {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &loginPATHRun{started: s.clock(), cancel: cancel, end: make(chan struct{}), gate: s.gate}
	s.run = r
	go s.read(ctx, r)
	return true
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
			logDebugf("[shellenv] Extracted shell PATH (%d chars) in %s, after %d failed read(s)", len(path), took, s.failures)
		}
	default:
		s.failures++
		logWarnf("[shellenv] Could not read PATH from the login shell after %s (%s): %v", took, s.failureNoteLocked(), err)
	}
	s.mu.Unlock()
	close(r.end)
}

// extract runs the extractor. A panic in it fails the read and does not end the
// daemon, as a panic of a request does not (claustrum's own).
func (s *loginPATHState) extract(ctx context.Context) (path string, err error) {
	defer func() {
		if v := recover(); v != nil {
			logErrorf("[shellenv] recovered panic in the read of the login-shell PATH: %v", v)
			path, err = "", fmt.Errorf("recovered panic: %v", v)
		}
	}()
	return loginPATHExtractor(ctx)
}

// failureNoteLocked records when the next read can start, and renders the note of
// the failure line.
func (s *loginPATHState) failureNoteLocked() string {
	if s.failures >= loginPATHMaxFailures {
		// The text of this note is claustrum's own (not measured).
		return fmt.Sprintf("failure %d; no further try; programs get the daemon's own PATH", s.failures)
	}
	pause := loginPATHFirstPause << (s.failures - 1)
	s.nextTry = s.clock().Add(pause)
	return fmt.Sprintf("failure %d; next try in %s at the earliest; until one works, programs get the daemon's own PATH",
		s.failures, pause)
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
		logInfof("[shellenv] Stopping the read of the login shell's PATH at the desktop's word " +
			"(disableShellAgentSocket); spawns that carry it start no other after a failure")
	}
	r.stopped = true
	s.mu.Unlock()
	r.cancel()
	waitUpTo(r.end, loginPATHStopWait)
}

// stopLoginPATH ends a running read at the shutdown. No read starts after it. It
// changes nothing when the read is not armed, as in a test server.
func stopLoginPATH() {
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
