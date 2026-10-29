//go:build unix

package main

import (
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// openNonBlocking is added to the open flags of the trust check's reads. A FIFO named
// HEAD or commondir is then judged at once, with no wait for a writer.
const openNonBlocking = syscall.O_NONBLOCK

// nonDirGitDirIsNoRepo is true on Linux and macOS. There a git directory that exists
// but is not a directory answers "no repository". Examples are a linked-worktree entry
// replaced by a file and a GIT_DIR that names a `.git` file. Measured against f6010b97 on a Linux VM.
const nonDirGitDirIsNoRepo = true

// registrationsLinkRefused is true on Linux and macOS. A registrations folder
// (`<git dir>/worktrees`) that is a symbolic link then takes no delete. A remove
// refuses it (cell C3 of round tier0-24, Linux VM, and of round tier0-25m,
// macOS VM).
const registrationsLinkRefused = true

// entryLinkRefusedLate is true on Linux and macOS. A create whose baseRepo is a
// linked worktree with a symbolic link as its entry gets the trust refusal as a
// failed add (cell C7 of round tier0-24, Linux VM). macOS is not measured.
const entryLinkRefusedLate = true

// gitDirPartUsable is the test of `objects` and `refs` in the git-directory test
// (isGitDirShape). On Linux and macOS it is search permission. So for a daemon that
// is not root a `refs` folder of mode 0600 fails, and git.info answers "no
// repository" there (build 52d82f0a, measured on Linux and macOS). Each other place
// that uses the test, and `objects` of mode 0600, are claustrum's own, not measured.
func gitDirPartUsable(p string) bool {
	return unix.Access(p, unix.X_OK) == nil
}

// sameGitDirPath compares two lexically cleaned paths as plain strings. A backslash is
// an ordinary file-name character here, so `..\..` in a commondir does not match.
func sameGitDirPath(a, b string) bool {
	return a == b
}

// resolveGitDirLinks resolves the symlinks in the git directory g before the check
// judges it. A refusal then names the resolved path.
func resolveGitDirLinks(g string) string {
	if r, err := filepath.EvalSymlinks(g); err == nil {
		return r
	}
	return g
}

// gitVersionDir is the working folder of the git calls outside a repository: the
// `git version` run, the probe, the version gate, the excludes read and the read of
// config text. It is the root folder on Linux and macOS (claustrum's own, not
// measured).
const gitVersionDir = "/"

// nonFolderStartRefuses is false on Linux and macOS. A configuration listing that
// cannot start in a path that is not a folder is no refusal there, and the method goes
// on (unenterableDir). It is a variable so that a test on Linux can set it.
var nonFolderStartRefuses = false

// walkStartsAsSpelled is false on Linux and macOS. A dir that filepath.EvalSymlinks
// cannot resolve starts no walk there (walkStart). It is a variable so that a test on
// Linux can set it.
var walkStartsAsSpelled = false
