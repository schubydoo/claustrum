package sockroute

import (
	"net"
	"time"
)

// On Windows a socket path is used as it is. No long-path route applies there.

func Listen(path string) (net.Listener, error) { return net.Listen("unix", path) }

func DialUnixPath(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}

func DialSocketInDaemon(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}

func DialSiblingSocket(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}

// KeepSocketFileAtClose does nothing on Windows: the close of the listener removes the
// socket path there, as before.
func KeepSocketFileAtClose(net.Listener) {}
