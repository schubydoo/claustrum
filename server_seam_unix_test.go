//go:build unix

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The two sentinels the seams below fail with, so each arm's stderr line is
// asserted against text this file owns rather than against a substring of some
// real OS error.
var (
	errChmodRefused = errors.New("seam: chmod refused")
	errStartRefused = errors.New("seam: start refused")
)

// captureStderr runs fn with os.Stderr redirected into a pipe and returns what
// was written. Mirrors TestDaemonizeTimeoutPrintsToStderr, which asserts the
// launcher's other stderr line the same way.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() {
		_ = w.Close()
		os.Stderr = old
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	_ = r.Close()
	return string(out)
}

// A socket whose 0600 tightening fails is reported and then carried past: the
// boot continues, the token is still persisted and the daemon still serves. That
// is the whole content of the arm — it neither returns an error nor closes the
// listener — so the assertions are the warning text plus the boot completing.
func TestNewServerOnSocketChmodFailureIsNonFatal(t *testing.T) {
	oldChmod := chmodSocket
	chmodSocket = func(string, os.FileMode) error { return errChmodRefused }
	t.Cleanup(func() { chmodSocket = oldChmod })

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "c.sock")

	var s *server
	var bootErr error
	stderr := captureStderr(t, func() {
		s, bootErr = newServerOnSocket(sock, "chmod-token", "", wireLogOptions{}, false, false)
	})
	if bootErr != nil {
		t.Fatalf("newServerOnSocket = %v, want the chmod failure to be non-fatal", bootErr)
	}
	if s == nil {
		t.Fatal("newServerOnSocket returned a nil server for a non-fatal chmod failure")
	}
	t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })

	want := "claustrum: chmod socket: " + errChmodRefused.Error()
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	// The arm does not unbind: the socket is still there and the token was still
	// persisted beside it, which is what "non-fatal" has to mean.
	if _, err := os.Stat(sock); err != nil {
		t.Errorf("socket missing after a failed chmod: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, persistedTokenName)); err != nil || string(b) != "chmod-token" {
		t.Errorf("%s = %q (err %v), want the boot to have carried on to persistToken", persistedTokenName, b, err)
	}
}

// When the re-exec of our own executable cannot even be started there is no
// daemon and nothing to wait for, so the launcher reports "daemonize: <err>" and
// exits 1 immediately — it must not fall through to the wait-for-accept line,
// which reports a different failure entirely.
func TestDaemonizeChildStartFailure(t *testing.T) {
	stubOsExit(t)
	oldStart := startDaemonChild
	startDaemonChild = func(*exec.Cmd) error { return errStartRefused }
	t.Cleanup(func() { startDaemonChild = oldStart })

	sock := filepath.Join(shortTempDir(t), "d.sock")
	var code int
	var exited bool
	stderr := captureStderr(t, func() {
		code, exited = catchExit(func() { daemonizeWithToken(sock, "") })
	})

	if !exited || code != 1 {
		t.Errorf("daemonizeWithToken with an unstartable child: exited=%v code=%d, want exit 1", exited, code)
	}
	want := "daemonize: " + errStartRefused.Error()
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	if strings.Contains(stderr, "timeout waiting for daemon to accept") {
		t.Errorf("stderr = %q, want no wait-for-accept report after a failed start", stderr)
	}
}
