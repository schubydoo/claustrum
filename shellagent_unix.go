//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The login-shell SSH agent hand-off (reference f6010b97). Every value below
// is reference behavior, so it is parity and always on, not a tunable. Each was
// measured on macOS or Linux VMs, side by side with the reference unless noted:
//
//	agentProbeTimeout   4s    a login shell that does not exit fails at 4.0-4.2 s
//	agentProbeWaitDelay 0.5s  a grandchild holding stdout after a good line
//	                          costs about 0.5 s and still succeeds
//	agentProbeBound     5s    a shell in a frozen cgroup, which the kill cannot
//	                          end, fails at 5.0 s ("could not be stopped")
//	agentDialTimeout    250ms a socket slowed to 240 ms is live, one slowed to
//	                          260 ms "does not accept connections"
//	agentDialBound      500ms one slowed to 490 ms still returns, one slowed to
//	                          510 ms "never returned"
//	agentReprobeAfter   10m   "will try again in 10m0s", no run at 590 s, a run
//	                          at 610 s
//	agentMaxFailures    3     tries 1, 2 and 3, then no further run (measured on
//	                          the reference; claustrum's side is a unit test);
//	                          a success in between starts the count again
//
// Variables, not constants, so tests can shrink them.
var (
	agentProbeTimeout   = 4 * time.Second
	agentProbeWaitDelay = 500 * time.Millisecond
	agentProbeBound     = 5 * time.Second
	agentDialTimeout    = 250 * time.Millisecond
	agentDialBound      = 500 * time.Millisecond
	agentReprobeAfter   = 10 * time.Minute
)

const agentMaxFailures = 3

// agentHandOff is the per-daemon state of the hand-off. One instance serves
// every connection, and its lock is held across a probe, so concurrent spawns
// that need a socket wait for the one probe in flight.
type agentHandOff struct {
	mu       sync.Mutex
	lastRun  time.Time // when the login shell last ran; zero until the first run
	failures int       // consecutive runs that yielded no usable socket; pinned at agentMaxFailures once the hand-off has ended
	sock     string    // the last socket the shell exported, live or not

	// Seams for tests.
	runShell func() (string, error)
	clock    func() time.Time
	dial     func(string) (live, hung bool)
	gate     chan struct{} // the login-shell gate that the PATH read shares
}

var agentHandOffState = &agentHandOff{
	runShell: askLoginShellForAgent,
	clock:    time.Now,
	dial:     dialAgentSocket,
	gate:     loginShellGate,
}

func defaultShellAgentSocket() string { return agentHandOffState.socket() }

// socket returns a socket to hand to a child, or "".
//
// A cached socket with a live answer of dialAgentSocket is returned with no probe
// and no log, even if its agent has died since. Once that answer is gone, it is
// dialed again on every spawn, so an agent restarted at the same path is picked up
// with no probe. Otherwise the login shell runs again only when it has never
// run or ran at least agentReprobeAfter ago, and only while fewer than
// agentMaxFailures runs in a row have failed. A dial that has not returned
// within agentDialBound ends the hand-off for the life of the daemon, even if it
// would have succeeded later.
//
// While another login shell runs, such as the PATH read, the step runs no shell.
// Measured on Linux and macOS against 52d82f0a: a spawn during a slow PATH
// read gets no SSH_AUTH_SOCK, no agent run starts during the read, and a later spawn
// gets the socket. claustrum's own, not measured: that step counts no try and logs
// nothing.
func (s *agentHandOff) socket() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sock != "" {
		live, hung := s.dial(s.sock)
		if live {
			return s.sock
		}
		if hung {
			s.stopForGoodLocked(s.sock)
			return ""
		}
	}
	due := s.lastRun.IsZero() || s.clock().Sub(s.lastRun) >= agentReprobeAfter
	if !due || s.failures >= agentMaxFailures || !tryGate(s.gate) {
		return ""
	}
	if s.sock != "" {
		logWarnf("[shellenv] %s=%s no longer accepts connections; running the login shell again",
			agentSockVar, s.sock)
	}
	// The gate goes free also when runShell panics.
	sock, err := func() (string, error) {
		defer func() { <-s.gate }()
		return s.runShell()
	}()
	s.lastRun = s.clock()
	if err != nil {
		s.failures++
		logWarnf("[shellenv] Could not read %s from the login shell (%s): %v",
			agentSockVar, s.failureNoteLocked(), err)
		return ""
	}
	if sock == "" {
		s.sock = ""
		s.failures++
		logInfof("[shellenv] The login shell exports no %s (%s)", agentSockVar, s.failureNoteLocked())
		return ""
	}
	s.sock = sock
	live, hung := s.dial(sock)
	if hung {
		s.stopForGoodLocked(sock)
		return ""
	}
	if live {
		s.failures = 0
		logInfof("[shellenv] The login shell exports %s=%s", agentSockVar, sock)
		return sock
	}
	s.failures++
	logWarnf("[shellenv] The login shell exports %s=%s, which does not accept connections (%s)",
		agentSockVar, sock, s.failureNoteLocked())
	return ""
}

// failureNoteLocked renders the "(try N of 3; …)" note of a failure log line.
func (s *agentHandOff) failureNoteLocked() string {
	if s.failures >= agentMaxFailures {
		return fmt.Sprintf("try %d of %d; not running it again", s.failures, agentMaxFailures)
	}
	return fmt.Sprintf("try %d of %d; will try again in %s at the earliest",
		s.failures, agentMaxFailures, agentReprobeAfter)
}

// stopForGoodLocked stops the hand-off for good after a dial that had not
// returned within agentDialBound: the socket is dropped and the failure budget is spent, so no later
// spawn dials it or runs the login shell again.
func (s *agentHandOff) stopForGoodLocked(sock string) {
	s.sock = ""
	s.failures = agentMaxFailures
	logWarnf("[shellenv] A connection attempt to %s=%s never returned (a hung filesystem?); "+
		"not using it or asking the login shell again for this daemon's lifetime", agentSockVar, sock)
}

// loginShellError is why a login-shell run yielded no socket. Its text is
// what the "Could not read" log line carries. It holds no text that the shell
// printed, only byte counts (measured on Linux and macOS against 52d82f0a).
type loginShellError struct {
	shell, reason string
}

func (e *loginShellError) Error() string { return "login shell " + e.shell + " " + e.reason }

// askLoginShellForAgent runs the login shell once and returns the
// SSH_AUTH_SOCK it exports: a path, or "" when it exports none.
//
// It runs like extractLoginPATH in shellenv_unix.go in how it starts the shell
// (same shell choice, `-l -i -c`, own process group, killed as a group, the same
// output caps), but differs on purpose in how it reads the result. Only stdout is
// searched: measured, a marker and path printed only on stderr hands on nothing, on
// both daemons. A good line wins even when the deadline fired or a grandchild held the
// pipe open. Measured: a shell that prints a good line and then hangs still succeeds,
// once the deadline ends the run.
func askLoginShellForAgent() (string, error) {
	shell := safeLoginShell()
	// A fresh sentinel per run, so fixed profile output cannot match it by accident.
	sentinel, err := freshMarker("___CLAUDE_SSH_AGENT_SOCK_")
	if err != nil {
		return "", err
	}
	script := `/bin/sh -c 'printf "%s\n%s\n" "` + sentinel + `" "$SSH_AUTH_SOCK"'`

	ctx, cancel := context.WithTimeout(context.Background(), agentProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", script)
	cmd.Env = probeShellEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = agentProbeWaitDelay
	o := newShellOutput()
	cmd.Stdout, cmd.Stderr = &o.stdout, &o.stderr

	runErr, finished := waitAtMost(agentProbeBound, cmd.Run)
	if !finished {
		// The kill did not end the run. Its buffers are still being written,
		// so nothing of them is read.
		return "", &loginShellError{shell: shell,
			reason: "did not finish within " + agentProbeTimeout.String() + " and could not be stopped"}
	}
	if line, found := valueAfterMarker(o.stdout.kept.String(), sentinel); found {
		if line == "" || line[0] == '/' {
			return line, nil
		}
		// The byte counts of this text are claustrum's own (not measured).
		return "", &loginShellError{shell: shell,
			reason: "printed something other than a path for SSH_AUTH_SOCK (" + o.counts() + ")"}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", &loginShellError{shell: shell, reason: "did not finish within " + agentProbeTimeout.String()}
	}
	if o.stdout.cut || o.stderr.cut || (runErr != nil && !errors.Is(runErr, exec.ErrWaitDelay)) {
		return "", &loginShellError{shell: shell,
			reason: "exited (" + o.endReason(runErr) + ") without printing SSH_AUTH_SOCK (" + o.counts() + ")"}
	}
	return "", &loginShellError{shell: shell, reason: "did not print SSH_AUTH_SOCK (" + o.counts() + ")"}
}

// valueAfterMarker returns the trimmed line that follows the marker line in
// out, and whether the marker line was there at all.
func valueAfterMarker(out, marker string) (string, bool) {
	i := strings.Index(out, marker+"\n")
	if i < 0 {
		return "", false
	}
	rest := out[i+len(marker)+1:]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest), true
}

// probeShellEnv builds the probe shell's env from the daemon's own. It
// drops the daemon's CLAUDE_SSH_* markers and the SSH session variables, then
// sets three variables that quieten shell frameworks and let a profile tell it
// is being resolved. Measured: a daemon-env CLAUDE_SSH_ variable and the SSH_*
// session variables are absent in the probe shell, and a daemon
// DISABLE_AUTO_UPDATE=false reaches it as true.
func probeShellEnv(base []string) []string {
	out := make([]string, 0, len(base)+3)
	for _, e := range base {
		if strings.HasPrefix(e, "CLAUDE_SSH_") ||
			strings.HasPrefix(e, "SSH_CONNECTION=") ||
			strings.HasPrefix(e, "SSH_CLIENT=") ||
			strings.HasPrefix(e, "SSH_TTY=") {
			continue
		}
		out = append(out, e)
	}
	out = replaceOrAppendEnv(out, "DISABLE_AUTO_UPDATE", "true")
	out = replaceOrAppendEnv(out, "ZSH_DISABLE_COMPFIX", "true")
	return replaceOrAppendEnv(out, "CLAUDE_DESKTOP_RESOLVING_ENVIRONMENT", "1")
}

// dialUnix is the dial dialAgentSocket makes. A seam, so a test can make it
// hang the way a dial into a stuck filesystem does.
var dialUnix = net.DialTimeout

// agentLiveTTL is how long a live answer for one socket is kept. Measured on
// Linux against 683cc6a9: a live answer still holds at 60 s with no connect,
// and is gone at 610 s. The value 600 s is claustrum's own, not measured.
var agentLiveTTL = 600 * time.Second

// agentLiveKey names one socket file. The key of path, device, inode and
// modification time is claustrum's own, not measured. Measured on Linux against
// 683cc6a9: a new inode at the same path is dialed again, and in one run so was a socket
// that reused the old inode.
type agentLiveKey struct {
	path       string
	dev, ino   uint64
	mtimeNanos int64
}

var agentLive struct {
	sync.Mutex
	key     agentLiveKey
	until   time.Time
	present bool
}

// statAgent is the stat of dialAgentSocket. A seam, so a test can make it hang.
var statAgent = os.Stat

// agentLiveNow is the clock of the live-answer cache. A seam for tests.
var agentLiveNow = time.Now

// dialAgentSocket reports whether path accepts a unix-socket connection
// within agentDialTimeout (live), and whether the attempt had not returned
// within agentDialBound (hung). A path that is not a socket is dead with no
// connect call (measured on Linux against 683cc6a9). A live answer is kept for
// agentLiveTTL and reused with no connect while the socket file is unchanged.
func dialAgentSocket(path string) (live, hung bool) {
	if path == "" {
		return false, false
	}
	// The stat and the dial share one bound, because a stuck filesystem can
	// hang either. The seams are read once: the goroutine can outlive this call.
	statFn, dialFn, timeout := statAgent, dialUnix, agentDialTimeout
	type result struct {
		live, cached bool
		key          agentLiveKey
	}
	res, finished := waitAtMost(agentDialBound, func() result {
		fi, err := statFn(path)
		if err != nil || fi.Mode()&os.ModeSocket == 0 {
			return result{}
		}
		key := agentLiveKey{path: path, mtimeNanos: fi.ModTime().UnixNano()}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			key.dev, key.ino = uint64(st.Dev), uint64(st.Ino) //nolint:unconvert // the field types differ by system
		}
		agentLive.Lock()
		cached := agentLive.present && agentLive.key == key && agentLiveNow().Before(agentLive.until)
		agentLive.Unlock()
		if cached {
			return result{live: true, cached: true}
		}
		c, err := dialFn("unix", path, timeout)
		if err != nil {
			return result{}
		}
		_ = c.Close()
		return result{live: true, key: key}
	})
	if !finished {
		return false, true
	}
	if res.live && !res.cached {
		agentLive.Lock()
		agentLive.key, agentLive.until, agentLive.present = res.key, agentLiveNow().Add(agentLiveTTL), true
		agentLive.Unlock()
	}
	return res.live, false
}

// waitAtMost runs f and waits at most d for it. On a timeout it returns the
// zero value and false, and leaves f running: a call that is wedged in the
// kernel cannot be interrupted, only abandoned.
func waitAtMost[T any](d time.Duration, f func() T) (T, bool) {
	done := make(chan T, 1)
	go func() { done <- f() }()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case v := <-done:
		return v, true
	case <-t.C:
		var zero T
		return zero, false
	}
}
