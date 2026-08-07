//go:build !windows

package main

import (
	"errors"
	"net"
)

// honorListenPipe forces -listen-pipe OFF everywhere except Windows. The
// named-pipe transport exists only because a Python asyncio client cannot consume
// an AF_UNIX socket on Windows; on every other platform such clients use the
// socket directly, so the flag has no meaning. Rather than fail, we ignore it and
// warn — the same shape as honorKeepChildren's Windows no-op.
func honorListenPipe(requested bool) bool {
	if requested {
		logWarnf("[Server] -listen-pipe is only supported on Windows and is ignored: other platforms serve JSON-RPC over the AF_UNIX socket directly")
	}
	return false
}

// isOurClose reports whether a connection read failed because the daemon itself
// closed the connection (what closeAll does to every client on the graceful
// shutdown path) rather than because the read genuinely failed. Off Windows the
// only transport is the AF_UNIX socket, so the net-package sentinel is the whole
// answer.
func isOurClose(err error) bool { return errors.Is(err, net.ErrClosed) }

// startPipeTransport is never reached off Windows (honorListenPipe forces the flag
// false before server.run consults it), but must exist so server.go compiles on
// every target.
func startPipeTransport(string) (net.Listener, error) {
	return nil, errors.New("named-pipe transport is Windows-only")
}
