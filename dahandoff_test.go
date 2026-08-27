package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// removeSocketIfOwned must NOT delete a socket a successor rebound (different inode)
// but MUST remove one this daemon still owns. Matches the reference: 7d193f89 leaves a
// successor's rebound socket alone rather than deleting it out from under the new
// daemon. Exercised with regular files, since os.SameFile keys on inode identity
// regardless of file type.
//
// Unix only: the successor-rebind arm relies on a delete-and-recreate at the same
// path yielding a DIFFERENT identity, which holds for a POSIX inode but not on
// Windows, where NTFS reuses the file index so os.SameFile reports the recreated
// file as the same. The production guard keys on inode identity (os.SameFile); the
// reference's socket handoff is likewise inode-based, so both inherit that same
// Windows limitation — this test just cannot assert the rebind distinction there.
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

// isSocketDead classifies a dial error: a missing socket file or a refused
// connection means nothing is listening (stale socket), anything else is treated as
// "possibly live" so the launcher keeps waiting for a genuine handoff.
//
// The "wrapped not exist" row is the one that guards the OpError-unwrap bug:
// net.DialTimeout wraps its errno in *net.OpError, which os.IsNotExist does not
// unwrap, so the old os.IsNotExist(err) arm returned false for the ENOENT a vanished
// socket produces. errors.Is walks the chain and syscall.Errno.Is maps ENOENT onto
// fs.ErrNotExist on every OS.
func TestIsSocketDead(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"not exist", os.ErrNotExist, true},
		{"wrapped not exist", &net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ENOENT}}, true},
		{"refused", &net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}, true},
		{"timeout", context.DeadlineExceeded, false},
		{"opaque", errors.New("something else"), false},
	}
	for _, c := range cases {
		if got := isSocketDead(c.err); got != c.want {
			t.Errorf("%s: isSocketDead(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}

// isConnRefused / isSocketDead must recognise a dial to a stale AF_UNIX socket
// (the file is present but nothing is accepting) as dead, on every OS. This dials a
// REAL stale socket so each platform's own refused errno is exercised: ECONNREFUSED
// on POSIX, WSAECONNREFUSED (10061) on the Windows CI leg — where the ECONNREFUSED
// arm alone never matches, so this is what guards the Windows-specific arm.
//
// A short os.MkdirTemp prefix keeps the socket path under the AF_UNIX sun_path limit
// (~104-108 bytes) on every runner, incl. macOS's long /var/folders temp roots.
func TestIsConnRefusedStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")

	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen(unix): %v", err)
	}
	// Leave the socket file on disk after Close so the dial hits a stale socket with
	// nothing accepting (a crash-leftover), not a missing path.
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, derr := net.DialTimeout("unix", sock, time.Second)
	if derr == nil {
		t.Fatal("dial to a stale socket unexpectedly succeeded")
	}
	if !isConnRefused(derr) {
		t.Errorf("isConnRefused(%v) = false, want true", derr)
	}
	if !isSocketDead(derr) {
		t.Errorf("isSocketDead(%v) = false, want true", derr)
	}
}
