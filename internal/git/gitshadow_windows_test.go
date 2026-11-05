//go:build windows

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShadowLockHasNoSharing pins the hold of the daemon on its lock file on
// Windows. A Windows test measured the lock file of a live 5fd08069 daemon. Every
// open with data access fails there, with every share mode. The file cannot be
// removed. An open for attributes works (measured).
//
// A Windows run must show four things while the daemon holds the file.
// os.OpenFile fails for a read and for a write. os.Remove fails. os.Lstat
// works. After the daemon closed the file, the open and the remove work.
// os.OpenFile shares read, write and delete. So it is the open that passed
// when the daemon held the file through os.OpenFile.
func TestShadowLockHasNoSharing(t *testing.T) {
	path := filepath.Join(t.TempDir(), shadowLockName)
	lock, err := openShadowLockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	held := true
	t.Cleanup(func() {
		if held {
			_ = lock.Close()
		}
	})
	for name, flag := range map[string]int{"read": os.O_RDONLY, "write": os.O_WRONLY, "read and write": os.O_RDWR} {
		if f, err := os.OpenFile(path, flag, 0); err == nil {
			_ = f.Close()
			t.Errorf("an open for %s of the held lock file works, want a sharing error", name)
		}
	}
	if err := os.Remove(path); err == nil {
		t.Error("the held lock file was removed")
	}
	if fi, err := os.Lstat(path); err != nil || fi.Size() != 0 {
		t.Errorf("a look at the held lock file: %v, %v, want a file of 0 bytes", fi, err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	held = false
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("an open after the daemon closed the file: %v", err)
	}
	_ = f.Close()
	if err := os.Remove(path); err != nil {
		t.Errorf("a remove after the daemon closed the file: %v", err)
	}
}

// TestShadowLockErrorIsTheSystemText pins the cause of a lock file that cannot be
// made on Windows: the text of the system alone, with no operation and no path.
// With a deny entry for writes on git-shadow and the backup and restore
// privileges on, b3081b51 answers `(Access is denied.)` in the start test text
// (measured on Windows). A missing folder gives the same shape here.
func TestShadowLockErrorIsTheSystemText(t *testing.T) {
	lock, err := openShadowLockFile(filepath.Join(t.TempDir(), "gone", shadowLockName))
	if err == nil {
		_ = lock.Close()
		t.Fatal("the lock file was made in a missing folder")
	}
	if got, want := err.Error(), "The system cannot find the path specified."; got != want {
		t.Errorf("the error = %q, want %q", got, want)
	}
}
