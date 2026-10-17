//go:build !linux && !darwin

package main

import "net"

// connPeerPID is 0 on the other systems. livePredecessorPID is then 0 too, so the
// first connect that works ends the launcher wait.
func connPeerPID(net.Conn) int { return 0 }
