package main

import (
	"errors"
	"io/fs"
	"os"
)

// Daemon-to-daemon socket handoff, matching 7d193f89. A unix socket path cannot be
// rebound in place, so a restarting daemon unlinks the old socket and binds a fresh
// inode; the launcher must wait for that NEW inode to accept, and a departing
// predecessor must not delete a successor's socket. All three pieces key on
// os.SameFile inode identity of the socket file — no pid file, signal, or lock.
//
// livePredecessorIdent is OS-split (livepred_unix.go / livepred_windows.go): the
// dial-based detection runs on unix; on Windows it is a nil stub, since the probe has
// no observable effect there (measured — a live-predecessor second launch is the same
// ~0.01s either way, matching the reference).

// isSocketDead reports whether a dial error means nothing is listening (a stale
// socket file: the path exists but no process is accepting), as opposed to a
// transient error where a live daemon may still be present.
//
// net.DialTimeout wraps its errno in *net.OpError, which os.IsNotExist does not
// unwrap — so a dial that fails ENOENT (the socket vanished between the os.Stat
// and the dial) reads as os.IsNotExist=false. errors.Is walks the OpError chain
// and syscall.Errno.Is maps ENOENT onto fs.ErrNotExist, so use that instead. The
// refused-connection arm is OS-specific (isConnRefused): Windows reports a refused
// AF_UNIX dial as WSAECONNREFUSED, not the ECONNREFUSED that POSIX uses.
func isSocketDead(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || isConnRefused(err)
}

// removeSocketIfOwned unlinks the socket on graceful shutdown ONLY when the socket
// on disk is still the same inode this daemon bound (owned). If a successor has
// already rebound the path to a new inode, the departing daemon leaves it alone —
// matching 7d193f89, so a restart's old daemon cannot delete the new daemon's
// socket. A nil owned (never recorded) falls back to the unconditional unlink.
func removeSocketIfOwned(socket string, owned os.FileInfo) {
	if owned == nil {
		_ = os.Remove(socket)
		return
	}
	cur, err := os.Stat(socket)
	if err != nil {
		return // already gone
	}
	if os.SameFile(cur, owned) {
		_ = os.Remove(socket)
	}
}
