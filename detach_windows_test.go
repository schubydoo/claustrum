//go:build windows

package main

import (
	"slices"
	"testing"

	"github.com/schubydoo/claustrum/internal/supervisor"
)

// TestDaemonStartEnvDropsMarksOnWindows: with
// CLAUDE_SSH_CHILD and CLAUDE_SSH_RUN_DIR in the environment of the launcher, the
// daemon of 87375cc7 and 13047a51 holds neither name (measured on Windows). The other
// entries keep their order.
func TestDaemonStartEnvDropsMarksOnWindows(t *testing.T) {
	env := []string{"A=1", supervisor.EnvChildMarker + "=x", supervisor.EnvRunDir + `=C:\w\x`, "B=2"}
	if got, want := daemonStartEnv(env), []string{"A=1", "B=2"}; !slices.Equal(got, want) {
		t.Errorf("daemonStartEnv = %q, want %q", got, want)
	}
}
