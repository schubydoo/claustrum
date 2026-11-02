//go:build !windows

package renameretry

import (
	"os"
	"syscall"
	"testing"
)

// TestRenameHeldIsWindowsOnly: on Linux and macOS no rename error is tried
// again, also not a denied one.
func TestRenameHeldIsWindowsOnly(t *testing.T) {
	for _, err := range []error{syscall.EACCES, syscall.EBUSY, syscall.EPERM} {
		if Held(&os.LinkError{Op: "renameat", Old: "a", New: "b", Err: err}) {
			t.Errorf("Held(%v) = true, want false", err)
		}
	}
}
