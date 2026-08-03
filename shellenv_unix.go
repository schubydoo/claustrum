//go:build unix

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
)

// loginPATHTimeout caps the time the login-shell subprocess may run. The
// reference uses 4s — measured two ways against 5db5e4a: its extractPathFromShell
// carries a 4e9 ns timer immediate, and with a login shell that sleeps 6s the
// reference answers the first process.spawn after 4.01s (having given up) while
// a 10s cap answers after 5.82s (having waited). The value is load-bearing: on a
// host whose login shell takes longer than this, children inherit the daemon's
// PATH rather than the login PATH, so a longer cap diverges both in first-spawn
// latency and in every spawned child's environment.
// A variable so tests can override it without affecting production behavior.
var loginPATHTimeout = 4 * time.Second

// fallbackShells is the candidate list walked when $SHELL is not usable. The
// reference prefers **zsh**, then bash, then sh.
//
// CORRECTION, 2026-08-02: this list shipped as {bash, zsh, sh}, and the comment
// here said the bash-vs-zsh order "could not be measured (no /bin/zsh on the
// probe host)" and followed the reference's string-table order. Both halves were
// wrong. String-table order is not preference order, and the question is
// measurable on a host with no zsh — you supply one.
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

// pathSentinel brackets the PATH value echoed from the login shell.
const pathSentinel = "___CLAUDE_SSH_PATH_EXTRACT___"

// pathExtractCmd is the exact inner command run under the login shell.
const pathExtractCmd = `/bin/sh -c 'printf "%s%s\n" "___CLAUDE_SSH_PATH_EXTRACT___" "$PATH"'`

// extractLoginPATH runs the user's login shell interactively to capture the PATH
// it resolves, so spawned children inherit a real interactive PATH. Auto-update /
// compfix noise is suppressed. Best-effort: failures leave the existing PATH.
func extractLoginPATH() {
	shell := safeLoginShell()
	ctx, cancel := context.WithTimeout(context.Background(), loginPATHTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", pathExtractCmd)
	// Own process group so all children (e.g. tools the shell forks) are killed
	// together when the context times out, allowing CombinedOutput to return.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// timedOut records that the DEADLINE killed the command, rather than asking
	// the clock afterwards. os/exec calls Cancel only when the context finishes
	// before Wait completes (watchCtx selects between the two and returns without
	// calling Cancel when Wait wins), so setting it here IS "the deadline fired".
	//
	// Reading ctx.Err() after CombinedOutput returns answers a different
	// question — "is the deadline in the past NOW?" — and a shell that printed a
	// good sentinel and exited at 3.999s would lose its PATH to any scheduling gap
	// (a GC pause, a loaded host) that crossed the 4s boundary before the check.
	// That is the opposite of the divergence this function exists to fix, and it
	// is likeliest on exactly the hosts whose login shell already sits near the
	// cap. Not airtight — if Wait and the deadline land in the same instant
	// watchCtx still picks arbitrarily — but it narrows the window to true
	// simultaneity instead of "however long the code in between takes".
	var timedOut atomic.Bool
	cmd.Cancel = func() error {
		timedOut.Store(true)
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Env = append(os.Environ(),
		"DISABLE_AUTO_UPDATE=true",
		"ZSH_DISABLE_COMPFIX=true",
	)
	out, err := cmd.CombinedOutput()
	// A timeout DISCARDS whatever the shell printed, even a valid sentinel line,
	// and reports itself as one line naming the shell. Measured 2026-08-02
	// against 5db5e4a with a login shell that prints a good sentinel and THEN
	// sleeps past the cap:
	//
	//	reference : child PATH does NOT contain the extracted value
	//	            "[shellenv] Failed to extract PATH from login shell:
	//	             shell PATH extraction timed out (/bin/bash)"
	//	claustrum : child PATH DOES contain it, over two log lines, neither
	//	            of which says "timed out"
	//
	// So this is wire-visible too, not log-only: the child's environment differs.
	// The check must come BEFORE the sentinel scan below, because the scan is
	// what would otherwise install the discarded value.
	if timedOut.Load() {
		logWarnf("[shellenv] Failed to extract PATH from login shell: "+
			"shell PATH extraction timed out (%s)", shell)
		return
	}
	if err != nil {
		logWarnf("[shellenv] Shell command exited with error (may still have PATH): %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		i := strings.Index(line, pathSentinel)
		if i < 0 {
			continue
		}
		path := strings.TrimSpace(line[i+len(pathSentinel):])
		if path != "" {
			// Recorded for spawned children only — NOT installed into the daemon's
			// own environment. See loginPATH in shellenv.go.
			setLoginPATH(path)
			logDebugf("[shellenv] Extracted shell PATH (%d chars)", len(path))
		}
		return
	}
	// The reference wraps this failure and includes the shell's own output, which
	// is what makes it diagnosable — without the output there is no way to tell a
	// silent shell from a chatty one that simply never printed the sentinel.
	// Measured at 5db5e4a: "[shellenv] Failed to extract PATH from login shell:
	// PATH sentinel not found in shell output: no sentinel here".
	logWarnf("[shellenv] Failed to extract PATH from login shell: "+
		"PATH sentinel not found in shell output: %s",
		truncateShellOutput(strings.TrimRight(string(out), "\n")))
}

// shellOutputLogLimit is where the reference cuts the shell output it echoes
// into the failure log above, before appending "...".
//
// Measured 2026-08-02 against 5db5e4a with a login shell printing 500 bytes and
// no sentinel: the logged payload was exactly 203 characters and ended in "...",
// i.e. 200 kept plus the ellipsis. claustrum echoed all 500. Without the cut a
// chatty shell writes its whole profile output into the daemon log.
//
// The fixture was ASCII, so whether the reference counts bytes or runes is NOT
// established. Bytes is what a Go slice expression gives and is what this does;
// a multi-byte fixture would settle it and has not been run.
const shellOutputLogLimit = 200

// truncateShellOutput cuts s to shellOutputLogLimit and marks it as cut.
//
// The cut walks back to a rune boundary. Byte-slicing has a failure mode that is
// independent of bytes-vs-runes: a localised profile banner, a glyph or a UTF-8
// path can put byte 200 mid-sequence, and the log line then carries a truncated
// code point before the ellipsis — worse than either counting rule. Walking back
// moves the cut by at most three bytes and leaves ASCII output byte-identical to
// what was measured, so the 203-byte observation still holds.
func truncateShellOutput(s string) string {
	if len(s) <= shellOutputLogLimit {
		return s
	}
	cut := shellOutputLogLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
