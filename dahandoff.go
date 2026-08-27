package main

import (
	"errors"
	"net"
	"os"
	"syscall"
	"time"
)

// Daemon-to-daemon socket handoff, matching 7d193f89. A unix socket path cannot be
// rebound in place, so a restarting daemon unlinks the old socket and binds a fresh
// inode; the launcher must wait for that NEW inode to accept, and a departing
// predecessor must not delete a successor's socket. All three pieces key on
// os.SameFile inode identity of the socket file — no pid file, signal, or lock.

// livePredecessorIdent returns the socket file's identity when a LIVE daemon is
// already serving it, or nil when there is none (missing socket, or a stale socket
// whose dial is refused). Run before spawning the successor so waitForDaemonAccept
// can tell the predecessor's inode from the successor's.
func livePredecessorIdent(socket string) os.FileInfo {
	fi, err := os.Stat(socket)
	if err != nil {
		return nil // no socket → no predecessor
	}
	c, err := net.DialTimeout("unix", socket, 200*time.Millisecond)
	if err == nil {
		_ = c.Close()
		return fi // a live daemon answers → its socket inode
	}
	// A refused / not-yet-there dial means the socket is stale (no live daemon);
	// treat every other dial error conservatively as "live" so we still wait for a
	// genuine handoff rather than racing a possibly-live predecessor.
	if isSocketDead(err) {
		return nil
	}
	return fi
}

// isSocketDead reports whether a dial error means nothing is listening (a stale
// socket file: the path exists but no process is accepting), as opposed to a
// transient error where a live daemon may still be present.
func isSocketDead(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, syscall.ECONNREFUSED)
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
