//go:build windows

package main

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// isConnRefused reports whether a dial error means the connection was refused —
// a stale socket file with nothing accepting. On Windows a refused AF_UNIX connect
// surfaces as WSAECONNREFUSED (10061), which is a distinct syscall.Errno from the
// synthetic syscall.ECONNREFUSED, so errors.Is(err, syscall.ECONNREFUSED) alone
// never matches it; match both. (Measured: a dial to a stale or missing unix socket
// on Windows returns WSAECONNREFUSED, never ECONNREFUSED or ENOENT.)
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, windows.WSAECONNREFUSED)
}
