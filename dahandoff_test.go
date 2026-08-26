package main

import (
	"os"
	"path/filepath"
	"testing"
)

// removeSocketIfOwned must NOT delete a socket a successor rebound (different inode)
// but MUST remove one this daemon still owns — 7d193f89's guard against a departing
// daemon deleting the new daemon's socket. Exercised with regular files, since
// os.SameFile keys on inode identity regardless of file type.
func TestRemoveSocketIfOwned(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "rpc.sock")
	write := func() { _ = os.WriteFile(sock, nil, 0o600) }

	write()
	owned, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	// A successor replaces the path with a NEW inode.
	_ = os.Remove(sock)
	write()

	// The old owner must leave the successor's inode alone.
	removeSocketIfOwned(sock, owned)
	if _, err := os.Stat(sock); err != nil {
		t.Errorf("removeSocketIfOwned deleted a successor's socket: %v", err)
	}
	// The current owner removes its own inode.
	cur, _ := os.Stat(sock)
	removeSocketIfOwned(sock, cur)
	if _, err := os.Stat(sock); err == nil {
		t.Error("removeSocketIfOwned did not remove its own socket")
	}
	// A nil record falls back to an unconditional remove.
	write()
	removeSocketIfOwned(sock, nil)
	if _, err := os.Stat(sock); err == nil {
		t.Error("removeSocketIfOwned(nil) should unlink unconditionally")
	}
}
