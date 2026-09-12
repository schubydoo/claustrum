//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

// ignoreSigpipeDefault tells the runtime to discard SIGPIPE, so a write to a closed
// stdout returns EPIPE rather than killing the process. signal.Ignore covers fd 1/2,
// which the runtime would otherwise let terminate the process.
func ignoreSigpipeDefault() {
	signal.Ignore(syscall.SIGPIPE)
}
