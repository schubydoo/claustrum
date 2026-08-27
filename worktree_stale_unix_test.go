//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// resolveAsFarAsExists returns the canonical path directly when the path exists
// and contains a symlink (the fast path, before any ancestor-splitting).
func TestResolveAsFarAsExistsExistingSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if got, want := resolveAsFarAsExists(link), canonicalPath(real); got != want {
		t.Errorf("resolveAsFarAsExists(link) = %q, want %q", got, want)
	}
}
