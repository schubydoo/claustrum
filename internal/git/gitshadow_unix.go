//go:build unix

package git

import (
	"errors"
	"os"
	"runtime"
	"syscall"
)

// shadowLinksWorktrees is true where the copy of a repository has the link
// `worktrees`. 5fd08069 makes that link on Linux and macOS (measured).
const shadowLinksWorktrees = true

// stagedAddWindowsSteps is false on Linux and macOS. An add that names the copy
// is staged there too, as be1c1ca5 does, but after the steps of worktreeAdd
// before the add, and with the texts of Linux and macOS (runAddStaged). It is
// a variable so that a test can switch the steps of Windows on.
var stagedAddWindowsSteps = false

// doorReadBeforeListing is true on Linux and macOS. The config read of the ssh
// door of a create comes before the second listing of the checkout, and that
// listing gets the door entries (c9125eb8 and b3081b51, measured on Linux and
// macOS, hardenedGitCheckout).
const doorReadBeforeListing = true

// gitDirMustBeReal is true on Linux and macOS. A create holds the git directory
// that its gate accepted, and tests at its steps that the path still names it
// (acceptedGitDir). On macOS, 5fd08069 refuses a create whose `.git` became a
// symbolic link to another folder after `rev-parse --absolute-git-dir`, and that
// folder keeps every entry as it was (measured on macOS). Linux has the same
// rows (measured). A `.git` entry that is a symbolic link before the request is
// resolved by the trust check (resolveGitDirLinks), so the held folder is the
// folder behind that link, and gitDirLinkDetail finds no link. It is a variable
// so that a test can switch the steps off.
var gitDirMustBeReal = true

// openHeldGitDir opens the git directory at path as the root that a create
// holds until it answers (acceptGitDir). An open folder stops no rename and no
// remove of it on Linux and macOS.
func openHeldGitDir(path string) (*os.Root, error) {
	return os.OpenRoot(path)
}

// statusFollowsPrintedGitDir is false on Linux and macOS. The trust check
// resolves the links of the request path there, and a path through a link gets
// one folder (measured on macOS).
const statusFollowsPrintedGitDir = false

// probeConfigFreeRun is true on Linux and macOS. After a first call about the
// repository that exited 128 and a second run that exited 0, the call runs a
// third time with no config of the user or of the system (configFreeProbe).
// 52d82f0a does so on Linux and macOS. It also turns on the git-directory
// test after a good answer (askGit).
const probeConfigFreeRun = true

// checkoutDirPrefix starts the name of the folder of the checkout of one create
// in the folder of the daemon (runWorktreeCheckout). On Linux and macOS
// 4c111e86 names it `.checkout-<n>` with a decimal number, and the index of the
// checkout is in it (measured). claustrum uses the same name in the state
// fallback-git (claustrum's own, not measured).
const checkoutDirPrefix = ".checkout-"

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
// the file. On Linux /proc/locks shows `FLOCK ADVISORY WRITE <daemon pid>` for
// that file of 5fd08069 (measured). macOS has no such record. A lock held
// elsewhere fails the step. On a mount whose flock answers ENOLCK, c9125eb8
// goes on and every git request succeeds (measured on Linux). claustrum
// follows: after the errors of flockGoesOn it keeps the open file with no lock
// (takeFlock). The open follows no link at the last name, as on 5fd08069
// (`O_NOFOLLOW`, Linux).
func openShadowLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := takeFlock(f); err != nil {
		_ = f.Close()
		return nil, &os.PathError{Op: "flock", Path: path, Err: err}
	}
	return f, nil
}

// flockTries is the most flock calls of takeFlock while each answers EINTR
// (claustrum's own, not measured).
const flockTries = 8

// takeFlock takes a lock with no wait on f. locked is false with no error
// after an error of flockGoesOn. Every other error comes back, so the caller
// keeps its refusal. After EINTR the lock is tried again, up to flockTries
// calls, and then the EINTR comes back (claustrum's own, not measured).
func takeFlock(f *os.File) (locked bool, err error) {
	for range flockTries {
		err = flockNoWait(f)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case flockGoesOn(err):
			return false, nil
		default:
			return false, err
		}
	}
	return false, err
}

// flockGoesOn reports whether a flock error lets the step go on with no lock.
// c9125eb8 goes on after ENOLCK on Linux and after ENOTSUP of a FIFO lock on
// macOS (measured). Any other error keeps the refusal (claustrum's own, not
// measured).
func flockGoesOn(err error) bool {
	if runtime.GOOS == "darwin" {
		return errors.Is(err, syscall.ENOTSUP)
	}
	return errors.Is(err, syscall.ENOLCK)
}

// flockNoWait is the flock call with no wait. A variable, so that a test can
// give an error of the system.
var flockNoWait = func(f *os.File) error {
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
// of a folder while it removes the folder (not measured). In a measurement
// on Linux, 5fd08069 takes the lock and then removes the folder.
const sweepClosesLockFirst = false

// sweepAgeBeforeLock is false on Linux and macOS. The age of a folder with
// another host form is read after the lock step there (measured on Linux
// and macOS).
const sweepAgeBeforeLock = false

// isSweepLink reports whether an entry of git-shadow is a link. The sweep
// removes a link as a link (sweepLink).
func isSweepLink(fi os.FileInfo) bool {
	return fi.Mode()&os.ModeSymlink != 0
}

// takeSweepLock opens the lock file rel of a folder in git-shadow, through
// root, with create and mode 0600, and takes a lock with no wait on it. The
// open fails at a lock in use by another daemon or by another process with
// flock, and the folder then stays (measured on Linux). A file that is open
// with no flock does not stop it (measured). After an error of flockGoesOn,
// locked is false (takeFlock), and sweepEntry decides by name and age.
func takeSweepLock(root *os.Root, _, rel string) (f *os.File, locked bool, err error) {
	f, err = root.OpenFile(rel, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if locked, err = takeFlock(f); err != nil {
		_ = f.Close()
		return nil, false, err
	}
	return f, locked, nil
}

// sweepLockMayBeFIFO is true on macOS. There c9125eb8 removes a stale folder
// whose lock is a FIFO (flock gives ENOTSUP) at 10 days, and keeps a new one
// (measured). On Linux a FIFO lock keeps the folder (not measured).
const sweepLockMayBeFIFO = runtime.GOOS == "darwin"

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

// goneNotARepoKeepsAll is false on Linux and macOS: a remove of a gone
// worktree whose call about the repository found no repository keeps the
// lock-check refusal (removeGoneWorktree). Not measured there on b3081b51. A
// variable, so that a test can run the Windows step.
var goneNotARepoKeepsAll = false

// printedPathAsIs is false on Linux and macOS: the "disagree" text shows an
// answer that is not clean as the cleaned path (askGit, not measured).
const printedPathAsIs = false
