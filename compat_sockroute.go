package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/schubydoo/claustrum/internal/sockroute"
)

// The names of internal/sockroute under the names that the code of package main
// uses.

type (
	socketTooLongError = sockroute.TooLongError
	changeBackError    = sockroute.ChangeBackError
)

func listenSocket(path string) (net.Listener, error) { return sockroute.Listen(path) }

func dialUnixPath(path string, timeout time.Duration) (net.Conn, error) {
	return sockroute.DialUnixPath(path, timeout)
}

func dialSocketInDaemon(path string, timeout time.Duration) (net.Conn, error) {
	return sockroute.DialSocketInDaemon(path, timeout)
}

func dialSiblingSocket(path string, timeout time.Duration) (net.Conn, error) {
	return sockroute.DialSiblingSocket(path, timeout)
}

func keepSocketFileAtClose(ln net.Listener) { sockroute.KeepSocketFileAtClose(ln) }

// exitOnChangeBack ends a launcher whose connect could not change back to the
// working folder, with exit 1 (claustrum's own, not measured). Other errors pass.
func exitOnChangeBack(err error) {
	var back *changeBackError
	if errors.As(err, &back) {
		fmt.Fprintf(os.Stderr, "claustrum: %v\n", err)
		osExit(1)
	}
}
