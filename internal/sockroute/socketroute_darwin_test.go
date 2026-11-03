package sockroute

import "testing"

// noFolderHandle is a no-op on macOS: it has no /proc route.
func noFolderHandle(*testing.T) {}

// The limit of macOS is the one in the text of `be1c1ca5` (measured on macOS).
func TestMaxSocketPathDarwin(t *testing.T) {
	if MaxSocketPath != 103 {
		t.Errorf("MaxSocketPath = %d, want 103", MaxSocketPath)
	}
}
