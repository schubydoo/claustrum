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

// pathSentinel brackets the PATH value echoed from the login shell.
const pathSentinel = "___CLAUDE_SSH_PATH_EXTRACT___"

// pathExtractCmd is the exact inner command run under the login shell.
const pathExtractCmd = `/bin/sh -c 'printf "%s%s\n" "___CLAUDE_SSH_PATH_EXTRACT___" "$PATH"'`

// extractLoginPATH runs the user's login shell interactively to capture the PATH
// it resolves, so spawned children inherit a real interactive PATH. Auto-update /
// compfix noise is suppressed. Best-effort: failures leave the existing PATH.
func extractLoginPATH() {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
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
			_ = os.Setenv("PATH", path)
			logDebugf("[shellenv] Extracted shell PATH (%d chars)", len(path))
		}
		return
	}
	logWarnf("[shellenv] PATH sentinel not found in shell output")
}
