//go:build windows

package main

import (
	"fmt"
	"net"
	"os"
)

// livePredecessorPID is 0 on Windows, so the launcher ends its wait at the first
// connect that works, also when the predecessor answers it. 5fd08069 does the same:
// in 10 of 10 starts over a live predecessor both binaries exited 0 while the path
// still led to the predecessor (round task006-01, cell C3, Windows VM).
func livePredecessorPID(string) int { return 0 }

// keepSocketFileAtClose does nothing on Windows: the close of the listener removes the
// socket path there, as before.
func keepSocketFileAtClose(net.Listener) {}

// socketChmodFailed prints one line for a failed chmod of the socket, and the start
// goes on. That is the rule that claustrum had before on every system. Nothing is
// measured on Windows.
func socketChmodFailed(_ net.Listener, _ string, _ os.FileInfo, err error) error {
	fmt.Fprintf(os.Stderr, "claustrum: chmod socket: %v\n", err)
	return nil
}
