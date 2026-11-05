//go:build !windows

package supervisor

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

// managedSettingsDirEnv is the env name of the managed settings folder of the
// end-to-end tests of package main. A test sets it in the env of the daemon.
const managedSettingsDirEnv = "CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR"

// resolveTestRoot gives a t.TempDir() root with its links resolved, as the daemon
// reports a path below it. That matters on macOS, where /var is a link to
// /private/var. It changes nothing on Linux.
func resolveTestRoot(t *testing.T, p string) string {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// ignoreSigterm makes the helper child ignore SIGTERM (the "ignore-term" mode).
func ignoreSigterm() { signal.Ignore(syscall.SIGTERM) }

// ignoreSighup makes the helper child ignore SIGHUP (the "ignore-hup" mode).
func ignoreSighup() { signal.Ignore(syscall.SIGHUP) }

// exitZeroOnSigterm makes the helper child exit with code 0 when SIGTERM comes (the
// "term-exit0" mode).
func exitZeroOnSigterm() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	go func() {
		<-ch
		os.Exit(0)
	}()
}
