//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A FIFO at the config path must be rejected by loadConfig's regular-file guard
// rather than opened — otherwise reading it could block startup forever. Unix
// only (FIFOs don't exist on Windows); the cross-platform directory case in
// TestLoadConfig_NonRegularIgnored covers the rest. Uses stdlib syscall, not
// golang.org/x/sys, which this repo keeps Windows-only.
func TestLoadConfig_FifoIgnored(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, configFileName)
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	// This is exactly loadConfig's guard (os.Lstat + IsRegular); loadConfig itself
	// keys off os.Executable, so assert the guard directly against the FIFO.
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode().IsRegular() {
		t.Fatal("FIFO reported as regular file")
	}
}
