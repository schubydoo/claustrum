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

// openShadowLockFile makes the lock file of the daemon (0600, 0 bytes) and
// takes the lock of the daemon on it. The daemon holds the lock until it closes
// the file. On a Linux VM /proc/locks shows `FLOCK  ADVISORY  WRITE <daemon
// pid>` for that file of 5fd08069 (s3p2b-edges-linux2 cell 44). macOS has no
// such record. A lock that the system refuses is not measured: its text is
// claustrum's own.
func openShadowLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, &os.PathError{Op: "flock", Path: path, Err: err}
	}
	return f, nil
}
