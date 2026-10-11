//go:build unix

package main

import (
	"os"
	"syscall"
)

// shadowLinksWorktrees is true where the copy of a repository has the link
// `worktrees`. 5fd08069 makes that link on Linux and macOS VMs (s3-linux cell
// A3, s3-macos cell A3).
const shadowLinksWorktrees = true

// holdShadowLock takes the lock of the daemon on its own lock file. The daemon
// holds it until it closes the file. On a Linux VM /proc/locks shows `FLOCK
// ADVISORY  WRITE <daemon pid>` for that file of 5fd08069 (s3p2b-edges-linux2
// cell 44). macOS has no such record.
func holdShadowLock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return &os.PathError{Op: "flock", Path: f.Name(), Err: err}
	}
	return nil
}
