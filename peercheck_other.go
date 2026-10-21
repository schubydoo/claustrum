//go:build !linux

package main

import (
	"errors"
	"net"
)

// daemonNSText names no namespaces on macOS and Windows. A daemon there answers
// "unavailable" and serves every caller, as 5fd08069 does (macOS and Windows VMs).
func daemonNSText() nsText { return nsText{} }

// lookupPeer is never reached on macOS and Windows: a gate with no own text
// admits every caller first.
func lookupPeer(net.Conn, nsText) (nsText, bool, error) {
	return nsText{}, false, errors.New("this system names no namespaces")
}
