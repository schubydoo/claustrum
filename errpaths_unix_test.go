//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
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

// files.read of a file that stats fine but cannot be opened surfaces the
// ReadFile error as -32603 (distinct from the soft exists:false for a missing
// path, which stat catches earlier).
func TestFilesReadUnreadableFile(t *testing.T) {
	skipIfRoot(t)
	p := filepath.Join(t.TempDir(), "sealed")
	if err := os.WriteFile(p, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	s := newTestServer()
	got := dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": p}))
	if !strings.Contains(got, `"code":-32603`) {
		t.Errorf("files.read unreadable = %s, want -32603", got)
	}
}

// extractTarGz's destDir preparation arms: a read-only parent denies both the
// RemoveAll of a pre-existing destDir and the MkdirAll of a fresh one.
func TestExtractTarGzDestDirDenied(t *testing.T) {
	skipIfRoot(t)
	// extractTarGz consumes its archive on every outcome, so each attempt
	// below gets a fresh one.
	freshArchive := func() string {
		archive := filepath.Join(t.TempDir(), "a.tar.gz")
		writeTgz(t, archive, []tgzEntry{{name: "f", body: "x"}}, 0)
		return archive
	}

	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	occupied := filepath.Join(parent, "dest")
	if err := os.WriteFile(occupied, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	// dest exists but its parent denies the unlink -> RemoveAll arm
	if _, err := extractTarGz(freshArchive(), occupied); err == nil ||
		!strings.Contains(err.Error(), "clean destDir") {
		t.Errorf("extractTarGz into undeletable dest = %v, want clean destDir error", err)
	}
	// dest absent and its parent denies the create -> MkdirAll arm
	if _, err := extractTarGz(freshArchive(), filepath.Join(parent, "fresh")); err == nil ||
		!strings.Contains(err.Error(), "mkdir destDir") {
		t.Errorf("extractTarGz into uncreatable dest = %v, want mkdir destDir error", err)
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
