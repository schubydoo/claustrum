//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// The bounds of one PATH read. Variables so tests can shrink them.
var (
	// loginPATHTimeout ends the shell of a read. claustrum's own, not measured: no
	// row ran a read past 10 s that failed. A read of 10 s gave its PATH on Linux and
	// macOS (52d82f0a).
	loginPATHTimeout = 30 * time.Second
	// loginPATHBound ends the wait for a read whose shell the kill cannot end
	// (claustrum's own, not measured).
	loginPATHBound = 31 * time.Second
	// loginPATHWaitDelay closes the output pipes this long after the shell ended.
	// Measured on Linux and macOS against 52d82f0a: a shell that leaves a process on
	// its stdout and ends at once gives its PATH, logged "in 500ms" and "in 600ms".
	loginPATHWaitDelay = 500 * time.Millisecond
)

// fallbackShells is the candidate list walked when $SHELL is not usable. The
// reference prefers **zsh**, then bash, then sh.
//
// CORRECTION, 2026-08-02: this list shipped as {bash, zsh, sh}, and the comment
// here said the bash-vs-zsh order "could not be measured (no /bin/zsh on the
// probe host)". That was wrong: the order is measurable on a host with no zsh if
// you supply one.
//
// Measured 2026-08-02 against 5db5e4a, two independent instruments agreeing:
//
//	stand-in shells bind-mounted over /usr/bin (bwrap, no root)  ref zsh, claustrum bash
//	real bash + zsh in a clean Ubuntu VM, markers in each        ref zsh, claustrum bash
//	  shell's own login profile
//
// Each run carried single-shell controls — with only one of the two executable,
// both binaries agree — so "they differ" is not a harness artifact.
//
// This is wire-REACHABLE, not cosmetic: the extracted PATH is installed into
// every spawned child's environment, so on a host with both shells and an
// unusable $SHELL the two daemons hand their children different PATHs.
//
// Reachable, but by a narrower route than "wire-visible" suggests, so do not
// over-claim it: the daemon resolves process.spawn's `command` against its OWN
// PATH, not against the child env, and extraction deliberately never touches
// the daemon's own environment. So this can never flip a spawn between a success
// frame and "executable file not found". It shows up only in the payload bytes
// of a child that resolves binaries itself, e.g. `sh -c`.
var fallbackShells = []string{"/bin/zsh", "/bin/bash", "/bin/sh"}

// safeLoginShell returns $SHELL when it is an executable file, else the first
// usable entry in fallbackShells.
//
// $SHELL must be EXECUTABLE, not merely non-empty. claustrum trusted the value
// and then failed the exec outright, extracting no PATH at all. Measured at
// 5db5e4a with a non-executable $SHELL: the reference still logged "Extracted
// shell PATH (262 chars)" and gave the child a full login PATH, while claustrum
// logged "fork/exec …: permission denied" and gave up, leaving children with the
// daemon's bare inherited PATH.
func safeLoginShell() string {
	if sh := os.Getenv("SHELL"); isExecutableFile(sh) {
		return sh
	}
	for _, sh := range fallbackShells {
		if isExecutableFile(sh) {
			return sh
		}
	}
	return "/bin/sh" // nothing usable found; let the exec fail and be logged
}

// isExecutableFile reports whether path is a regular file with an execute bit.
// An empty path is never executable, which folds the old empty-$SHELL check in.
func isExecutableFile(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// pathSentinelPrefix starts the fresh sentinel of each read. The inner command
// prints the sentinel and the PATH on two lines. Measured on Linux and macOS against
// 52d82f0a: the shell gets -l, -i, -c and this one command, and two starts of the
// daemon give two sentinels of 32 hex digits.
const pathSentinelPrefix = "___CLAUDE_SSH_PATH_"

// pathReadCommand is the inner command of a read with the sentinel s.
func pathReadCommand(s string) string {
	return `/bin/sh -c 'printf "%s\n%s\n" "` + s + `" "$PATH"'`
}

// freshMarker returns prefix, 32 random hex digits and "___".
func freshMarker(prefix string) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(nonce[:]) + "___", nil
}

// loginPATHFromShell is true where a login shell gives the PATH, so a good read logs
// its line.
const loginPATHFromShell = true

// extractLoginPATH runs the login shell of the user once and returns the PATH that it
// prints. The end of ctx kills the process group of the shell, as the time limit does.
//
// Measured on Linux and macOS against 52d82f0a:
//
//   - The line after the sentinel line, on stdout or on stderr, is the PATH. It must
//     end in a newline and hold one absolute directory, or the read fails.
//   - The exit status does not matter when that line is good.
//   - A read that a stop ended gives "shell PATH extraction timed out".
//   - No failure text holds what the shell printed. It gives byte counts.
//
// claustrum's own (not measured): stdout is searched first, a bad line in stdout
// decides the read, and the PATH line is kept as printed, with no trim.
func extractLoginPATH(ctx context.Context) (string, error) {
	shell := safeLoginShell()
	sentinel, err := freshMarker(pathSentinelPrefix)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, loginPATHTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", pathReadCommand(sentinel))
	// Its own process group, so the kill ends every process of the shell.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// os/exec calls Cancel only when the context ends before Wait returns. So the flag
	// says that the time limit or a stop ended the read, not a late clock read.
	var timedOut atomic.Bool
	cmd.Cancel = func() error {
		timedOut.Store(true)
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Env = loginPATHShellEnv()
	cmd.WaitDelay = loginPATHWaitDelay
	out := newShellOutput()
	cmd.Stdout, cmd.Stderr = &out.stdout, &out.stderr
	runErr, finished := waitAtMost(loginPATHBound, cmd.Run)
	if timedOut.Load() || !finished {
		return "", fmt.Errorf("shell PATH extraction timed out (%s)", shell)
	}
	for _, b := range [][]byte{out.stdout.kept.Bytes(), out.stderr.kept.Bytes()} {
		line, found, whole := pathLineAfter(b, sentinel)
		if !found {
			continue
		}
		if whole && hasAbsoluteDir(line) {
			// Kept as printed, with no trim (claustrum's own, not measured).
			return line, nil
		}
		return "", fmt.Errorf("the login shell's PATH line was cut short or had no absolute directory in it (%d bytes read)", len(line))
	}
	return "", fmt.Errorf("the login shell ended (%s) without printing a usable PATH (%s)", out.endReason(runErr), out.counts())
}

// pathLineAfter returns the text after the sentinel line, up to the next newline.
// found reports the sentinel line, and whole reports the newline after the text.
func pathLineAfter(b []byte, sentinel string) (line string, found, whole bool) {
	i := bytes.Index(b, []byte(sentinel+"\n"))
	if i < 0 {
		return "", false, false
	}
	rest := b[i+len(sentinel)+1:]
	if j := bytes.IndexByte(rest, '\n'); j >= 0 {
		return string(rest[:j]), true, true
	}
	return string(rest), true, false
}

// hasAbsoluteDir reports whether one entry of the PATH list p is absolute.
func hasAbsoluteDir(p string) bool {
	for _, d := range strings.Split(p, ":") {
		if strings.HasPrefix(d, "/") {
			return true
		}
	}
	return false
}

// loginPATHShellEnv is the environment of the login shell that extractLoginPATH runs.
// It holds eight entries and nothing else of the daemon environment.
//
// Measured on Linux and macOS against 52d82f0a: the shell gets
// CLAUDE_DESKTOP_RESOLVING_ENVIRONMENT=1, DISABLE_AUTO_UPDATE=true, HOME, PATH, SHELL,
// TERM (empty when the daemon has none), USER and ZSH_DISABLE_COMPFIX=true. The shell
// itself adds PWD. Measured on 89cb6289: LANG and CLAUDE_SSH_DAEMON_CHILD of the
// daemon do not reach it, and a daemon with no SHELL gives an empty entry. The agent
// shell has another environment (probeShellEnv).
//
// Not measured: the order of the entries, a daemon that has a TERM, and a daemon
// with no HOME, PATH or USER.
func loginPATHShellEnv() []string {
	env := make([]string, 0, 8)
	for _, name := range []string{"HOME", "PATH", "SHELL", "TERM", "USER"} {
		env = append(env, name+"="+os.Getenv(name))
	}
	return append(env, "DISABLE_AUTO_UPDATE=true", "ZSH_DISABLE_COMPFIX=true", "CLAUDE_DESKTOP_RESOLVING_ENVIRONMENT=1")
}

// The caps of what a login shell prints, for the PATH read and the agent run.
// Measured on Linux and macOS against 52d82f0a: 2 MiB on stdout cuts the read, and
// the log counts 1048576 bytes. 20 MiB on stderr before a good PATH line still gives
// the PATH. On macOS the writer wrote 16805888 bytes of stderr before it got EPIPE.
// The exact bounds are claustrum's own (not measured).
// Variables so tests can shrink them.
var (
	shellStdoutCap  = 1 << 20  // stdout bytes kept, and one more cuts stdout
	shellStderrKeep = 1 << 20  // stderr bytes kept
	shellStderrCap  = 16 << 20 // stderr bytes read, and one more cuts stderr
)

// errOutputCut is the write error that ends the copy of a stream at its cap. os/exec
// then closes its end of the pipe, so a later write of the shell fails.
var errOutputCut = errors.New("login shell output cut")

// cappedOutput keeps the first keep bytes of a stream and reads at most limit bytes.
type cappedOutput struct {
	kept        bytes.Buffer
	keep, limit int
	total       int
	cut         bool
}

func (c *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if c.total+n > c.limit {
		n = c.limit - c.total
		c.cut = true
	}
	if room := c.keep - c.kept.Len(); room > 0 {
		c.kept.Write(p[:min(n, room)])
	}
	c.total += n
	if c.cut {
		return n, errOutputCut
	}
	return n, nil
}

// shellOutput is the stdout and the stderr of one login-shell run.
type shellOutput struct{ stdout, stderr cappedOutput }

func newShellOutput() *shellOutput {
	return &shellOutput{
		stdout: cappedOutput{keep: shellStdoutCap, limit: shellStdoutCap},
		stderr: cappedOutput{keep: shellStderrKeep, limit: shellStderrCap},
	}
}

// endReason names how a run ended that printed no usable line. A cut stream wins over
// the exit status (measured on macOS against 52d82f0a for a shell that the cut ended).
// A clean exit gives "exit status 0" (claustrum's own, not measured).
func (o *shellOutput) endReason(runErr error) string {
	if o.stdout.cut || o.stderr.cut {
		return "cut off after it had printed more than 1 MiB on stdout or 16 MiB on stderr"
	}
	if runErr != nil && !errors.Is(runErr, exec.ErrWaitDelay) {
		return runErr.Error()
	}
	return "exit status 0"
}

// counts renders the kept bytes of both streams for a failure text.
func (o *shellOutput) counts() string {
	return fmt.Sprintf("%d bytes on stdout, %d on stderr", o.stdout.kept.Len(), o.stderr.kept.Len())
}
