//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

// extraNobodyListening matches the Windows-only Winsock error for a refused dial.
// A refused AF_UNIX dial on Windows surfaces WSAECONNREFUSED, not the POSIX
// syscall.ECONNREFUSED that isNobodyListening checks, so it is matched here. A
// sibling whose socket is refused counts as gone and does not gate the legacy sweep.
func extraNobodyListening(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED)
}
