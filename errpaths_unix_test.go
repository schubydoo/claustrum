//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The permission-driven error arms live here: they rely on POSIX mode bits
// actually denying access, which neither Windows ACL semantics nor a root
// runner honor the same way.

// skipIfRoot: chmod-based denial is a no-op for uid 0.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-denial tests are meaningless as root")
	}
}

// loadConfigFrom fail-safes to the zero config when claustrum.conf exists but
// cannot be opened — a broken config must never take the daemon down.
func TestLoadConfigFromUnreadable(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("metrics_addr=1.2.3.4:1\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if cfg := loadConfigFrom(dir); cfg != (config{}) {
		t.Errorf("loadConfigFrom unreadable conf = %+v, want zero config", cfg)
	}
}

// readTokenFD's error arms. Both descriptors here are bare syscall fds with no
// competing *os.File owner, so readTokenFD's internal Close is the only close
// (see TestReadTokenFD for why that ownership rule matters under coverage).
func TestReadTokenFDErrors(t *testing.T) {
	// A negative fd can't be wrapped at all.
	if _, err := readTokenFD(-1); err == nil {
		t.Error("readTokenFD(-1) succeeded, want invalid-descriptor error")
	}

	// A write-only descriptor wraps fine but fails at read time (EBADF).
	path := filepath.Join(t.TempDir(), "wronly")
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readTokenFD(fd); err == nil {
		t.Error("readTokenFD on write-only fd succeeded, want read error")
	}
}
