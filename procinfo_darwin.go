//go:build darwin

package main

import (
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The macOS process facts that the host cleaner and the run-dir lock use: the node
// identity and the signal of the hold.

// nodeID returns the macOS machine identity: the per-boot session UUID from sysctl
// kern.bootsessionuuid, trimmed of a trailing NUL and surrounding whitespace. Unlike
// Linux there is no pid-namespace component (macOS has no pid namespaces). It returns ""
// on sysctl error, which makes the signal guard refuse eviction.
func nodeID() string {
	uuid, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.Trim(uuid, "\x00"))
}

// hcHoldDaemon returns a send that signals the single process pid with kill. darwin has no
// pid file descriptor, so there is no hold and release does nothing. Which call the macOS
// reference uses to retire a daemon is not measured.
func hcHoldDaemon(pid int) (send func(syscall.Signal) error, release func()) {
	return func(sig syscall.Signal) error { return syscall.Kill(pid, sig) }, func() {}
}
