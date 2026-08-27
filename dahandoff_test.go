package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// removeSocketIfOwned must NOT delete a socket a successor rebound (different inode)
// but MUST remove one this daemon still owns — 7d193f89's guard against a departing
// daemon deleting the new daemon's socket. Exercised with regular files, since
// os.SameFile keys on inode identity regardless of file type.
//
// Unix only: the successor-rebind arm relies on a delete-and-recreate at the same
// path yielding a DIFFERENT identity, which holds for a POSIX inode but not on
// Windows, where NTFS reuses the file index so os.SameFile reports the recreated
// file as the same. The production guard uses os.SameFile exactly as the reference
// does, so it inherits that same Windows limitation — this test just cannot assert
// the rebind distinction there.
func TestRemoveSocketIfOwned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.SameFile cannot distinguish a same-path delete-recreate on Windows (NTFS file-index reuse)")
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
	// place — deterministic on POSIX. (Windows is skipped above: os.SameFile cannot
	// distinguish two files there regardless.)
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
