package main

import (
	"net"
	"time"
)

// On Windows a socket path is used as it is. No long-path route applies there.

func listenSocket(path string) (net.Listener, error) { return net.Listen("unix", path) }

func dialUnixPath(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}

func dialSocketInDaemon(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}
