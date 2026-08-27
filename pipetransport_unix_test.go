//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// removePipeNameFileIfOwned owning an rpc.pipe it cannot unlink logs and returns;
// shutdown must not fail on a read-only socket directory.
func TestRemovePipeNameFileIfOwnedRemoveError(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	sock := filepath.Join(dir, "rpc.sock")
	if err := writePipeNameFile(sock, `\\.\pipe\x`); err != nil {
		t.Fatal(err)
	}
	path := pipeNameFilePath(sock)
	owned, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	removePipeNameFileIfOwned(sock, owned)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should survive a failed unlink: %v", err)
	}
}
