//go:build windows

package renameretry

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// The Windows side of the retry of a held rename (measured on Windows). No
// test of this file ran on the host of the build: a Windows run must match
// the cell that each test names.

// TestRenameHeldOnWindows pins the two errors that the retry tries again:
// access denied and a sharing violation. Another error fails at once.
func TestRenameHeldOnWindows(t *testing.T) {
	for _, row := range []struct {
		err  syscall.Errno
		want bool
	}{
		{syscall.ERROR_ACCESS_DENIED, true},
		{windows.ERROR_SHARING_VIOLATION, true},
		{syscall.ERROR_FILE_NOT_FOUND, false},
		{syscall.ERROR_ALREADY_EXISTS, false},
		{windows.ERROR_DIR_NOT_EMPTY, false},
	} {
		if got := Held(&os.LinkError{Op: "renameat", Old: "a", New: "b", Err: row.err}); got != row.want {
			t.Errorf("Held(%v) = %v, want %v", row.err, got, row.want)
		}
	}
}

// TestCopyRenameErrorOnWindows pins the text form: `rename <full path of the
// temp file> <full path of the copy>: Access is denied.`.
func TestCopyRenameErrorOnWindows(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	err = CopyError(root, &os.LinkError{Op: "renameat", Old: ".tmp-1", New: "HEAD", Err: syscall.ERROR_ACCESS_DENIED})
	want := "rename " + filepath.Join(dir, ".tmp-1") + " " + filepath.Join(dir, "HEAD") + ": Access is denied."
	if err == nil || err.Error() != want || !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Errorf("CopyError = %v, want %s", err, want)
	}
	other := errors.New("other")
	if got := CopyError(root, other); got != other {
		t.Errorf("CopyError of another error = %v, want it unchanged", got)
	}
}
