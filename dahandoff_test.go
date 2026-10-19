package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// removeSocketIfOwned must NOT delete a socket a successor rebound (different inode)
// but MUST remove one this daemon still owns. Exercised with regular files, since
// os.SameFile keys on inode identity regardless of file type.
//
// Unix only. On Windows Go removes the socket path at the close of the listener, and
// the daemon records the socket with os.Stat. That identity reads the file id by the
// path at the compare, so it matches a successor's file. Both binaries remove the new
// rpc.sock at that stop (cell W3 of round task091-01, Windows VM).
func TestRemoveSocketIfOwned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows: Go removes the socket path at the listener close, and an os.Stat identity reads the file id by the path at the compare")
	}
	sock := filepath.Join(t.TempDir(), "rpc.sock")
	write := func() { _ = os.WriteFile(sock, nil, 0o600) }

	write()
	owned, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	// A successor replaces the path with a NEW inode. A delete-then-recreate at the same
	// path can reuse the freed inode (measured: flaky on the ubuntu CI runner), so create
	// the replacement ALONGSIDE the original to force a distinct inode, then move it into
	// place — deterministic on POSIX. (Windows is skipped above.)
	succ := filepath.Join(filepath.Dir(sock), "succ.sock")
	if err := os.WriteFile(succ, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(succ, sock); err != nil {
		t.Fatal(err)
	}

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
