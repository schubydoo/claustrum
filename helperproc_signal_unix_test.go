//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

// ignoreSigterm makes the helper child ignore SIGTERM, so process.killAndWait's
// graceful signal is a no-op and it must escalate to SIGKILL (see the
// "ignore-term" helper mode and TestKillAndWaitEscalation).
func ignoreSigterm() { signal.Ignore(syscall.SIGTERM) }
