//go:build !windows

package main

import (
	"net"
	"os"
	"time"
)

// livePredecessorIdent returns the socket file's identity when a LIVE daemon is
// already serving it, or nil when there is none (missing socket, or a stale socket
// whose dial is refused). Run before spawning the successor so waitForDaemonAccept
// can tell the predecessor's inode from the successor's.
//
// Unix only: on Windows the dial-based probe has no observable effect (measured — a
// live-predecessor second launch returns in ~0.01s either way, matching the
// reference), so livepred_windows.go returns nil and this detection runs on unix only.
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

// staleSocketIdent returns the identity of a socket file that is on disk before the
// launcher starts its child, or nil when the path holds no socket. waitForDaemonAccept
// uses it: while the path still holds that file and no daemon answers, the launcher
// waits. The new daemon binds only after the reap of its start, so the stale socket
// file of a killed daemon stays for that time. Measured on Linux against f6010b97 and
// 89cb6289 (row XA): the -serve command returns when the socket accepts, 2.06 s after
// its start, with a recorded child that ignores SIGTERM.
//
// A path that holds another kind of file gives nil, so the wait for it is as before:
// with a directory at the socket path the launcher of the reference exits 0 at once
// (5db5e4a, see waitForDaemonAccept).
func staleSocketIdent(socket string) os.FileInfo {
	fi, err := os.Stat(socket)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return nil
	}
	return fi
}
