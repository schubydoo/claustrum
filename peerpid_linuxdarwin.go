//go:build linux || darwin

package main

import "net"

// connPeerPID returns the pid of the process at the other end of a unix connection,
// or 0 when it cannot be read (peerOf: SO_PEERCRED on Linux, LOCAL_PEERPID on macOS).
// A pid of 1 or less reads as 0: a peer in another pid namespace gives 0 (claustrum's
// own, not measured).
func connPeerPID(c net.Conn) int {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0
	}
	if pid, _, ok := peerOf(uc); ok && pid > 1 {
		return pid
	}
	return 0
}
