package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// readTokenFD reads the auth token from an open fd and normalizes it exactly like
// the -token-file path (trailing newline stripped, leading/interior spaces kept),
// so -token-fd and -token-file accept byte-for-byte the same token bytes.
func TestReadTokenFD(t *testing.T) {
	// readTokenFD wraps the fd in an *os.File and closes it (it owns the fd, just
	// like the daemonized child owns its inherited token pipe). So we must hand it
	// a descriptor that nothing else owns: syscall.Open returns a bare fd with no
	// competing *os.File finalizer. Passing an os.Pipe/os.Open file's .Fd() here
	// instead would give the fd two owners — readTokenFD's Close plus the source
	// file's finalizer Close — and that double-close can, after fd-number reuse,
	// clobber an unrelated descriptor (in CI, the coverage runtime's _cover_.out,
	// surfacing as "bad file descriptor" at exit).
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("  s3kret-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// int(fd) is redundant on unix (syscall.Open returns int) but required on
	// Windows (it returns syscall.Handle); the lint runs on linux, so annotate it.
	got, err := readTokenFD(int(fd)) //nolint:unconvert // cross-platform: Windows fd is a Handle
	if err != nil {
		t.Fatalf("readTokenFD: %v", err)
	}
	if want := "  s3kret-token"; got != want {
		t.Errorf("readTokenFD = %q, want %q", got, want)
	}
}
