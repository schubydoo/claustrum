//go:build unix

package main

import (
	"path/filepath"
	"syscall"
)

// openNonBlocking is added to the open flags of the trust check's reads. A FIFO named
// HEAD or commondir is then judged at once, with no wait for a writer.
const openNonBlocking = syscall.O_NONBLOCK

// nonDirGitDirIsNoRepo is true on Linux and macOS. There a git directory that exists
// but is not a directory answers "no repository". Examples are a linked-worktree entry
// replaced by a file and a GIT_DIR that names a `.git` file. Measured against f6010b97 on a Linux VM.
const nonDirGitDirIsNoRepo = true

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
