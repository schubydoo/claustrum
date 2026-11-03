//go:build unix

package git

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// The test helpers of package main that the Linux and macOS tests of this
// package use, under the same names.

// resolveTestRoot canonicalizes a t.TempDir() root to the spelling the daemon
// will report for paths under it: symlink-resolved, which matters on macOS
// where the temp dir lives under /var -> /private/var. No-op on Linux.
func resolveTestRoot(t *testing.T, p string) string {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// skipIfRoot: chmod-based denial is a no-op for uid 0.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-denial tests are meaningless as root")
	}
}

// releaseFifoReader opens the FIFO for writing without a wait and closes it. A
// reader that waits in its open returns then.
func releaseFifoReader(path string) {
	if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
		_ = f.Close()
	}
}

// mustMkfifo creates a FIFO, and FAILS rather than skips on the two platforms CI
// runs this file on. Elsewhere (a temp filesystem that genuinely refuses FIFOs)
// the skip is still the right answer.
func mustMkfifo(t *testing.T, path string) {
	t.Helper()
	err := syscall.Mkfifo(path, 0o600)
	if err == nil {
		return
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		t.Fatalf("mkfifo %s: %v — this platform must support FIFOs, so a skip here "+
			"would silently drop the FIFO rows", path, err)
	}
	t.Skipf("mkfifo unavailable: %v", err)
}

// pollUntil polls cond every 5 ms until it holds or limit passes.
func pollUntil(limit time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(limit)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// dropGroupWrite clears the group and other write bits of dir and returns dir.
// Under umask 0002, t.TempDir() is mode 0775, and the worktreeRoot guard
// refuses a folder that its group can write. The fixtures call this on their
// temp folder, so the guard sees the mode that the test means.
func dropGroupWrite(t *testing.T, dir string) string {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, fi.Mode().Perm()&^0o022); err != nil {
		t.Fatal(err)
	}
	return dir
}
