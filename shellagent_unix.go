//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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

const (
	agentMaxFailures = 3
	// agentOutputLimit is where a failure message cuts the shell's output
	// before it appends "...". It is a plain byte cut, even inside a UTF-8
	// sequence: 199 ASCII bytes and then a 3-byte character echo as the 199
	// bytes, the character's first byte, and "...".
	agentOutputLimit = 200
)

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
}

var agentHandOffState = &agentHandOff{
	runShell: askLoginShellForAgent,
	clock:    time.Now,
	dial:     dialAgentSocket,
}

func defaultShellAgentSocket() string { return agentHandOffState.socket() }

// socket returns a socket to hand to a child, or "".
//
// A cached socket that still accepts a connection is returned with no probe
// and no log. A cached socket that no longer does stays cached, and is dialed
// again on every spawn, so an agent restarted at the same path is picked up
// with no probe. Otherwise the login shell runs again only when it has never
// run or ran at least agentReprobeAfter ago, and only while fewer than
// agentMaxFailures runs in a row have failed. A dial that has not returned
// within agentDialBound ends the hand-off for the life of the daemon, even if it
// would have succeeded later.
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
	if !due || s.failures >= agentMaxFailures {
		return ""
	}
	if s.sock != "" {
		logWarnf("[shellenv] %s=%s no longer accepts connections; running the login shell again",
			agentSockVar, s.sock)
	}
	sock, err := s.runShell()
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
// what the "Could not read" log line carries.
type loginShellError struct {
	shell, reason, output string
}

func (e *loginShellError) Error() string {
	msg := "login shell " + e.shell + " " + e.reason
	if len(e.output) > 0 {
		out := e.output
		if len(out) > agentOutputLimit {
			out = out[:agentOutputLimit] + "..."
		}
		msg += "; output began: " + strings.TrimSpace(out)
	}
	return msg
}

// askLoginShellForAgent runs the login shell once and returns the
// SSH_AUTH_SOCK it exports: a path, or "" when it exports none.
//
// It mirrors the PATH extraction in shellenv_unix.go in how it starts the shell
// (same shell choice, `-l -i -c`, own process group, killed as a group), but
// differs on purpose in how it reads the result. stdout and stderr are kept
// apart and only stdout is searched: measured, a marker and path printed only
// on stderr hands on nothing, on both daemons. A good line wins even when the deadline
// fired or a grandchild held the pipe open. Measured: a shell that prints a
// good line and then hangs still succeeds, once the deadline ends the run.
func askLoginShellForAgent() (string, error) {
	shell := safeLoginShell()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	// A fresh sentinel per run, so fixed profile output cannot match it by accident.
	sentinel := "___CLAUDE_SSH_AGENT_SOCK_" + hex.EncodeToString(nonce[:]) + "___"
	script := `/bin/sh -c 'printf "%s\n%s\n" "` + sentinel + `" "$SSH_AUTH_SOCK"'`

	ctx, cancel := context.WithTimeout(context.Background(), agentProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", script)
	cmd.Env = probeShellEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = agentProbeWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr, finished := waitAtMost(agentProbeBound, cmd.Run)
	if !finished {
		// The kill did not end the run. Its buffers are still being written,
		// so nothing of them is read.
		return "", &loginShellError{shell: shell,
			reason: "did not finish within " + agentProbeTimeout.String() + " and could not be stopped"}
	}
	out := stdout.String()
	if line, found := valueAfterMarker(out, sentinel); found {
		if line == "" || line[0] == '/' {
			return line, nil
		}
		return "", &loginShellError{shell: shell,
			reason: "printed something other than a path for SSH_AUTH_SOCK", output: line}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", &loginShellError{shell: shell, reason: "did not finish within " + agentProbeTimeout.String()}
	}
	both := out + stderr.String()
	if runErr != nil && !errors.Is(runErr, exec.ErrWaitDelay) {
		return "", &loginShellError{shell: shell,
			reason: "exited (" + runErr.Error() + ") without printing SSH_AUTH_SOCK", output: both}
	}
	return "", &loginShellError{shell: shell, reason: "did not print SSH_AUTH_SOCK", output: both}
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

// dialAgentSocket reports whether path accepts a unix-socket connection
// within agentDialTimeout (live), and whether the attempt had not returned
// within agentDialBound (hung).
func dialAgentSocket(path string) (live, hung bool) {
	if path == "" {
		return false, false
	}
	dialFn, timeout := dialUnix, agentDialTimeout // read once: the goroutine can outlive this call
	ok, finished := waitAtMost(agentDialBound, func() bool {
		c, err := dialFn("unix", path, timeout)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})
	return ok && finished, !finished
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
