//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShadowLockHasNoSharing pins the hold of the daemon on its lock file on
// Windows. On a Windows VM every open of the lock file of a live 5fd08069
// daemon with data access fails, with every share mode, and the file cannot be
// removed. An open for attributes works (val-s3p2b2-1-windows row D2).
//
// A Windows run must show: os.OpenFile for a read and for a write both fail
// while the daemon holds the file, os.Remove fails, os.Lstat works, and all
// three work after the daemon closed the file. os.OpenFile shares read, write
// and delete, so it is the open that the batch 1 build still let pass.
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
