//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"
)

// shadowLinksWorktrees is true where the copy of a repository has the link
// `worktrees`. 5fd08069 makes that link on Linux and macOS VMs (s3-linux cell
// A3, s3-macos cell A3).
const shadowLinksWorktrees = true

// stagedAddWindowsSteps is false on Linux and macOS. An add that names the copy
// is staged there too, as be1c1ca5 does, but after the steps of worktreeAdd
// before the add, and with the texts of Linux and macOS (runAddStaged).
const stagedAddWindowsSteps = false

// gitDirMustBeReal is true on Linux and macOS. A create holds the git directory
// that its gate accepted, and tests at its steps that the path still names it
// (acceptedGitDir). On a macOS VM, 5fd08069 refuses a create whose `.git`
// became a symbolic link to another folder after `rev-parse
// --absolute-git-dir`, and that folder keeps every entry as it was (cells
// swap-copy, swap-copyold and swap-marker of val-s3p2b2-9-macos). The Linux VM
// has the same rows (val-s3p2b2-9-linux2). A `.git` entry that is a symbolic link
// before the request is resolved by the trust check (resolveGitDirLinks), so
// the held folder is the folder behind that link, and gitDirLinkDetail finds
// no link. It is a variable so that a test can switch the steps off.
var gitDirMustBeReal = true

// openHeldGitDir opens the git directory at path as the root that a create
// holds until it answers (acceptGitDir). An open folder stops no rename and no
// remove of it on Linux and macOS.
func openHeldGitDir(path string) (*os.Root, error) {
	return os.OpenRoot(path)
}

// statusFollowsPrintedGitDir is false on Linux and macOS. The trust check
// resolves the links of the request path there, and a path through a link gets
// one folder (s3p2-macos cells B1 and B2, macOS VM).
const statusFollowsPrintedGitDir = false

// probeConfigFreeRun is true on Linux and macOS. After a first call about the
// repository that exited 128 and a second run that exited 0, the call runs a
// third time with no config of the user or of the system (configFreeProbe).
// 52d82f0a does so on Linux and macOS VMs. It also turns on the
// git-directory test after a good answer (askGit).
const probeConfigFreeRun = true

// makeRequestDir makes the short-lived folder name of a request (mode 0700) in
// the folder of the daemon. On Linux and macOS it goes through root, the root
// at folder (requestDir). No row has a deny entry with enabled privileges on
// Linux or macOS (not measured).
func makeRequestDir(root *os.Root, _, name string) error {
	return root.Mkdir(name, 0o700)
}

// listedSwap is the test of a create after the first listing of the checkout.
// On Linux and macOS no test runs there, so it reports no swap.
func (gitUse) listedSwap() (fileSwap, changed bool) {
	return false, false
}

// openShadowLockFile makes the lock file of the daemon (0600, 0 bytes) and
// takes the lock of the daemon on it. The daemon holds the lock until it closes
// the file. On a Linux VM /proc/locks shows `FLOCK  ADVISORY  WRITE <daemon
// pid>` for that file of 5fd08069 (s3p2b-edges-linux2 cell 44). macOS has no
// such record. A lock that the system refuses is not measured: its text is
// claustrum's own. The open follows no link at the last name, as on 5fd08069
// (`O_NOFOLLOW` in round task075-01, Linux VM).
func openShadowLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := flockNoWait(f); err != nil {
		_ = f.Close()
		return nil, &os.PathError{Op: "flock", Path: path, Err: err}
	}
	return f, nil
}

func flockNoWait(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// shadowLockBusy reports whether err of openShadowLockFile is a lock that
// another holder has (newOwnFolder).
func shadowLockBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK)
}

// shadowLockAt reports whether the file at path is still the open lock. On
// Linux and macOS another process can remove a lock file that is held
// (newOwnFolder).
func shadowLockAt(lock *os.File, path string) bool {
	held, err := lock.Stat()
	return err == nil && sameEntryAt(path, held)
}

// sweepClosesLockFirst is false on Linux and macOS. The sweep holds the lock
// of a folder while it removes the folder (not measured). In cell G1 of round
// task075-01 (Linux VM) 5fd08069 takes the lock and then removes the folder.
const sweepClosesLockFirst = false

// sweepAgeBeforeLock is false on Linux and macOS. The age of a folder with
// another host form is read after the lock step there (cells G7 and G7c of
// round task075-01, Linux and macOS VMs).
const sweepAgeBeforeLock = false

// isSweepLink reports whether an entry of git-shadow is a link. The sweep
// removes a link as a link (sweepLink).
func isSweepLink(fi os.FileInfo) bool {
	return fi.Mode()&os.ModeSymlink != 0
}

// takeSweepLock opens the lock file rel of a folder in git-shadow, through
// root, with create and mode 0600, and takes a lock with no wait on it. The
// open fails at a lock in use by another daemon or by another process with
// flock, and the folder then stays (cells G2 and G3 of round task075-01, Linux
// VM). A file that is open with no flock does not stop it (cell G4). Any
// other error of the lock keeps the folder too (claustrum's own, not measured).
func takeSweepLock(root *os.Root, _, rel string) (*os.File, error) {
	f, err := root.OpenFile(rel, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := flockNoWait(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// linkedLockOpen tests the lock file at path with a read open and a lock with
// no wait, and gives both back at once. The open follows no link at the last
// name and does not wait for a FIFO.
func linkedLockOpen(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return flockNoWait(f)
}
