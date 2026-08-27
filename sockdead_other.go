//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// isConnRefused reports whether a dial error means the connection was refused —
// a stale socket file with nothing accepting. On POSIX that is ECONNREFUSED.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
