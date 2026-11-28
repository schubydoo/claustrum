//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestRemoveRefusedTokenFileLinkAndFIFO pins the Linux rows of the token
// remove: a link goes as a link and its target stays, and a FIFO goes.
func TestRemoveRefusedTokenFileLinkAndFIFO(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	removeRefusedTokenFile(link)
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("link kept (%v)", err)
	}
	if _, err := os.Lstat(target); err != nil {
		t.Errorf("link target gone: %v", err)
	}
	// A link to a folder goes as a link too, and the folder stays (claustrum's
	// own, not measured).
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	dlink := filepath.Join(dir, "dlink")
	if err := os.Symlink(sub, dlink); err != nil {
		t.Fatal(err)
	}
	removeRefusedTokenFile(dlink)
	if _, err := os.Lstat(dlink); !os.IsNotExist(err) {
		t.Errorf("folder link kept (%v)", err)
	}
	if fi, err := os.Lstat(sub); err != nil || !fi.IsDir() {
		t.Errorf("folder behind the link gone (%v)", err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	removeRefusedTokenFile(fifo)
	if _, err := os.Lstat(fifo); !os.IsNotExist(err) {
		t.Errorf("FIFO kept (%v)", err)
	}
}
