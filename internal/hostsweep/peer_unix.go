//go:build linux || darwin

package hostsweep

import "net"

// PeerOf returns the pid and uid of the process on the other end of a unix connection
// (see peerOf).
func PeerOf(conn *net.UnixConn) (pid int, uid uint32, ok bool) { return peerOf(conn) }
