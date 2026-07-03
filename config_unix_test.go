//go:build unix

package main

import (
	"path/filepath"
	"syscall"
	"testing"
)

// A FIFO at the config path must be rejected by loadConfigFrom's regular-file
// guard rather than opened — otherwise reading it could block startup forever.
// Unix only (FIFOs don't exist on Windows); the directory case in
// TestLoadConfigFrom covers the rest. Uses stdlib syscall, not golang.org/x/sys,
// which this repo keeps Windows-only. If the guard regressed this test would
// hang, so it also asserts non-blocking behavior.
func TestLoadConfigFrom_FifoIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, configFileName), 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	if cfg := loadConfigFrom(dir); cfg != (config{}) {
		t.Fatalf("fifo config: got %+v, want zero config", cfg)
	}
}
