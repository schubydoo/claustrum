package main

import "os"

// Daemon-to-daemon socket handoff, a feature 7d193f89 added. A unix socket path cannot be
// rebound in place, so a restarting daemon unlinks the old socket and binds a fresh
// one, and a departing predecessor must not delete a successor's socket. On Linux and
// macOS the launcher half tells the two daemons apart by the peer pid of a connect
// (waitForDaemonAccept).

// removeSocketIfOwned unlinks the socket on graceful shutdown ONLY when the socket
// on disk is still the same inode this daemon bound (owned). If a successor has
// already rebound the path to a new inode, the departing daemon leaves it alone.
// A nil owned
// (never recorded) falls back to the unconditional unlink.
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
