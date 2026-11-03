package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// openDaemonLog is the platform-neutral half of sweep gap N1: daemonizeWithToken
// lives in server.go and is shared, so these semantics apply on Windows too,
// where the e2e in server_daemonize_unix_test.go cannot run.
func TestOpenDaemonLog(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	logPath := filepath.Join(dir, daemonLogName)

	f, err := openDaemonLog(sock)
	if f == nil || err != nil {
		t.Fatalf("openDaemonLog = %v, %v for a writable socket directory, want a file", f, err)
	}
	if _, err := f.WriteString("first run\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("log not created beside the socket: %v", err)
	}
	if runtime.GOOS != "windows" {
		// Windows does not model 0600 as an owner-only ACL — the same caveat
		// already documented for daemon.token.
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("mode = %o, want 600", got)
		}
	}

	// An EXISTING log with a loose mode must not be inherited. The 0o600 passed
	// to OpenFile applies only on creation, so truncating a pre-created 0666 file
	// would keep the daemon's output readable by other local users. openDaemonLog
	// removes first and creates exclusively. The reference gives the same result
	// (probe-measured: a planted 0666 file owned by ANOTHER user comes back 0600
	// owned by us — an owner change, which chmod cannot perform).
	if runtime.GOOS != "windows" {
		if err := os.Chmod(logPath, 0o666); err != nil {
			t.Fatal(err)
		}
		f3, err := openDaemonLog(sock)
		if f3 == nil || err != nil {
			t.Fatalf("openDaemonLog = %v, %v for an existing log, want a file", f3, err)
		}
		_ = f3.Close()
		fi, err := os.Stat(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("pre-existing 0666 log left at mode %o, want 600", got)
		}
	}

	// TRUNCATE, not append: the reference starts a fresh log every -serve.
	f2, err := openDaemonLog(sock)
	if f2 == nil || err != nil {
		t.Fatalf("second openDaemonLog = %v, %v, want a file", f2, err)
	}
	if _, err := f2.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	_ = f2.Close()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "second\n" {
		t.Errorf("log = %q, want %q — a restart must truncate, not append", b, "second\n")
	}
}

// An empty socket path opens no log and returns no error. A socket folder that does not exist
// returns the error of the create, which fails the start (a measurement shows that rule for a
// folder that cannot be written).
func TestOpenDaemonLogUnusablePath(t *testing.T) {
	if f, err := openDaemonLog(""); f != nil || err != nil {
		t.Errorf("empty socket path: openDaemonLog = %v, %v, want nil, nil", f, err)
	}
	missing := filepath.Join(t.TempDir(), "no-such-dir", "s.sock")
	if f, err := openDaemonLog(missing); f != nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing socket directory: openDaemonLog = %v, %v, want nil and a not-exist error", f, err)
	}
}
