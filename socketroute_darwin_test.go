package main

import "testing"

// noFolderHandle is a no-op on macOS: it has no /proc route.
func noFolderHandle(*testing.T) {}

// The limit of macOS is the one in the text of `be1c1ca5` (measured on macOS).
func TestMaxSocketPathDarwin(t *testing.T) {
	if maxSocketPath != 103 {
		t.Errorf("maxSocketPath = %d, want 103", maxSocketPath)
	}
}
