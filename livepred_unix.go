//go:build !windows

package main

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/schubydoo/claustrum/internal/sockroute"
)

// livePredecessorPID returns the pid of the daemon that answers a connect on the
// socket path, or 0 when no connect works or the pid cannot be read. The launcher
// runs it before it starts the child, so waitForDaemonAccept can tell a connect to
// that daemon from a connect to the new one. The 200 ms bound of that connect and
// the 0 result are not measured. A connect that could not change back to the
// working folder ends the launcher with exit 1 (claustrum's own, not measured).
func livePredecessorPID(socket string) int {
	c, err := sockroute.DialUnixPath(socket, 200*time.Millisecond)
	exitOnChangeBack(err)
	if err != nil {
		return 0
	}
	defer c.Close()
	return connPeerPID(c)
}

// socketChmodFailed ends a start whose chmod of the socket failed. It closes the listener,
// removes the socket path and returns the error of the start. `5fd08069` ends the start
// there with that text (measured on Linux). On macOS that is not measured. The remove of
// the socket path is claustrum's own, not measured: the measurement used an abstract
// socket, which has no path. The path goes only while it still is the socket that this
// start bound (unlinkOwnSocket). Another entry there stays.
func socketChmodFailed(ln net.Listener, socket string, bound os.FileInfo, err error) error {
	_ = ln.Close()
	unlinkOwnSocket(socket, bound)
	return fmt.Errorf("chmod socket: %v", err)
}
