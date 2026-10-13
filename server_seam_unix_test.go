//go:build unix

package main

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The two sentinels the seams below fail with, so each arm's error text is
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

// A socket whose 0600 tightening fails ends the start with the error "chmod socket:
// <error>", and nothing listens (`5fd08069`, cell H6, Linux VM). The socket path is
// gone too. The remove of the socket path is not measured. The start stops before the
// token is persisted. The control is the same start with the real chmod: it serves.
func TestNewServerOnSocketChmodFailureEndsTheStart(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "c.sock")

	control, err := newServerOnSocket(sock, "chmod-token", "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("control: newServerOnSocket = %v, want a start with the real chmod", err)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Errorf("control: no socket after a good start: %v", err)
	}
	control.signalShutdown()
	control.closeAll(sock)

	oldChmod := chmodSocket
	chmodSocket = func(string, os.FileMode) error { return errChmodRefused }
	t.Cleanup(func() { chmodSocket = oldChmod })

	s, bootErr := newServerOnSocket(sock, "chmod-token", "", wireLogOptions{}, false, false)
	if s != nil {
		t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })
		t.Error("newServerOnSocket returned a server after a failed chmod")
	}
	if want := "chmod socket: " + errChmodRefused.Error(); bootErr == nil || bootErr.Error() != want {
		t.Errorf("newServerOnSocket = %v, want the error %q", bootErr, want)
	}
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket path is still there after a failed chmod: %v", err)
	}
	if c, err := net.Dial("unix", sock); err == nil {
		_ = c.Close()
		t.Error("a listener still answers after a failed chmod")
	}
	if _, err := os.Lstat(filepath.Join(dir, persistedTokenName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s exists after a failed chmod: %v", persistedTokenName, err)
	}
}

// A failed chmod removes only the socket that this start bound. Here the chmod seam
// puts a plain file at the socket path before it fails. The start ends with the chmod
// error, and the file stays. TestNewServerOnSocketChmodFailureEndsTheStart is the
// control: there the path still holds our own socket, and it goes.
func TestNewServerOnSocketChmodFailureKeepsAnotherEntry(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "c.sock")

	oldChmod := chmodSocket
	chmodSocket = func(path string, _ os.FileMode) error {
		if err := os.Remove(path); err != nil {
			t.Errorf("remove the bound socket: %v", err)
		}
		if err := os.WriteFile(path, []byte("other"), 0o600); err != nil {
			t.Errorf("write the other entry: %v", err)
		}
		return errChmodRefused
	}
	t.Cleanup(func() { chmodSocket = oldChmod })

	s, bootErr := newServerOnSocket(sock, "chmod-token", "", wireLogOptions{}, false, false)
	if s != nil {
		t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })
		t.Error("newServerOnSocket returned a server after a failed chmod")
	}
	if want := "chmod socket: " + errChmodRefused.Error(); bootErr == nil || bootErr.Error() != want {
		t.Errorf("newServerOnSocket = %v, want the error %q", bootErr, want)
	}
	if b, err := os.ReadFile(sock); err != nil || string(b) != "other" {
		t.Errorf("the other entry at the socket path = %q, %v, want it left alone", b, err)
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
