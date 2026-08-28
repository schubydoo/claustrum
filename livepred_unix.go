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
