package git

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// A failed close of the new marker stops the create on Linux only (c9125eb8
// and 4c111e86, measured on Linux). On macOS and Windows the marker step goes
// on, as before (claustrum's own, not measured). The marker stays on every
// system.
func TestMarkerCloseFailureStopsOnLinuxOnly(t *testing.T) {
	dir := t.TempDir()
	old := closeMarkerFile
	t.Cleanup(func() { closeMarkerFile = old })
	closeMarkerFile = func(f *os.File) error {
		_ = f.Close()
		return &fs.PathError{Op: "close", Path: f.Name(), Err: syscall.EIO}
	}
	err := ensureManagedWorktreesMarker(dir)
	if stops := runtime.GOOS == "linux"; (err != nil) != stops {
		t.Errorf("ensureManagedWorktreesMarker = %v, want an error: %v", err, stops)
	}
	if b, rerr := os.ReadFile(filepath.Join(dir, managedWorktreesMarker)); rerr != nil || string(b) != managedWorktreesMarkerBody {
		t.Errorf("the marker = %q, %v, want its 285 bytes", b, rerr)
	}
}
