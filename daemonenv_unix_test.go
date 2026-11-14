//go:build unix

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDaemonEnvDropsLauncherMarks pins the environment of the daemonized child. A
// launcher with CLAUDE_SSH_CHILD, CLAUDE_SSH_RUN_DIR and CLAUDE_SSH_DAEMON_CHILD=0 gives
// a child with neither of the first two names, and CLAUDE_SSH_DAEMON_CHILD=1 is its last
// entry. Measured on Linux against c9125eb8 and 4c111e86.
func TestDaemonEnvDropsLauncherMarks(t *testing.T) {
	t.Setenv("CLAUDE_SSH_CHILD", "1")
	t.Setenv("CLAUDE_SSH_RUN_DIR", "/tmp/x")
	t.Setenv(daemonChildMarker, "0")
	stubOsExit(t)
	var env []string
	oldStart := startDaemonChild
	startDaemonChild = func(cmd *exec.Cmd) error { env = cmd.Environ(); return errStartRefused }
	t.Cleanup(func() { startDaemonChild = oldStart })

	sock := filepath.Join(shortTempDir(t), "d.sock")
	_ = captureStderr(t, func() { _, _ = catchExit(func() { daemonizeWithToken(sock, "") }) })

	if len(env) < 2 {
		t.Fatalf("the child environment = %q, want the two markers at its end", env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDE_SSH_CHILD=") || strings.HasPrefix(e, "CLAUDE_SSH_RUN_DIR=") {
			t.Errorf("the child environment holds %q, want it dropped", e)
		}
	}
	if got := env[len(env)-2:]; got[0] != daemonChildEnv+"=1" || got[1] != daemonChildMarker+"=1" {
		t.Errorf("the last two entries = %q, want %s=1 then %s=1", got, daemonChildEnv, daemonChildMarker)
	}
}
