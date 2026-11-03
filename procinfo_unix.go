//go:build linux || darwin

package main

import "syscall"

// The process group signal that the orphan reap and the host cleaner use.

var (
	// killGroup signals the whole process group led by pid (kill(-pid, sig)), the reap's
	// only destructive call.
	killGroup = func(pid int, sig syscall.Signal) error { return syscall.Kill(-pid, sig) }
)
