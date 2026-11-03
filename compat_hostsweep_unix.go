//go:build linux || darwin

package main

import (
	"net"

	"github.com/schubydoo/claustrum/internal/hostsweep"
)

// The linux and darwin name of internal/hostsweep under the name that the code of
// package main uses.

func peerOf(conn *net.UnixConn) (pid int, uid uint32, ok bool) { return hostsweep.PeerOf(conn) }
