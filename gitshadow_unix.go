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

// shadowStagesAdd is false on Linux and macOS. On a Linux VM no daemon makes a
// `.s<n>` folder or a `.claude-new-worktree-*` entry, and none removes a planted
// one (s3p2-linux cells H1 and H2). On a macOS VM no daemon made such an entry.
// No macOS cell planted one.
const shadowStagesAdd = false

// gitDirMustBeReal is false on Linux and macOS. No row has a create there whose
// git directory is replaced while it runs, and a `.git` entry that is a
// symbolic link is resolved by the trust check (resolveGitDirLinks). The steps
// of acceptedGitDir do not run. It is a variable so that a test can run them.
var gitDirMustBeReal = false

// statusFollowsPrintedGitDir is false on Linux and macOS. The trust check
// resolves the links of the request path there, and a path through a link gets
// one folder (s3p2-macos cells B1 and B2, macOS VM).
const statusFollowsPrintedGitDir = false

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
