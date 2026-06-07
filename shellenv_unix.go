//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

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
	cmd := exec.Command(shell, "-l", "-i", "-c", pathExtractCmd)
	cmd.Env = append(os.Environ(),
		"DISABLE_AUTO_UPDATE=true",
		"ZSH_DISABLE_COMPFIX=true",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[shellenv] Shell command exited with error (may still have PATH): %v\n", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		i := strings.Index(line, pathSentinel)
		if i < 0 {
			continue
		}
		path := strings.TrimSpace(line[i+len(pathSentinel):])
		if path != "" {
			_ = os.Setenv("PATH", path)
			fmt.Printf("[shellenv] Extracted shell PATH (%d chars)\n", len(path))
		}
		return
	}
	fmt.Fprintln(os.Stderr, "[shellenv] PATH sentinel not found in shell output")
}
