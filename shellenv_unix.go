//go:build unix

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
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

// fallbackShells is the candidate list the reference walks when $SHELL is not
// usable. Its getSafeShell references exactly these three paths (plus SHELL),
// and on a host where $SHELL was non-executable it produced a full bash-profile
// PATH — so /bin/bash is tried before /bin/sh. The bash-vs-zsh order could not
// be measured here (no /bin/zsh on the probe host) and follows the order the
// reference's string table lists them in.
var fallbackShells = []string{"/bin/bash", "/bin/zsh", "/bin/sh"}

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
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Env = append(os.Environ(),
		"DISABLE_AUTO_UPDATE=true",
		"ZSH_DISABLE_COMPFIX=true",
	)
	out, err := cmd.CombinedOutput()
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
		"PATH sentinel not found in shell output: %s", strings.TrimRight(string(out), "\n"))
}
