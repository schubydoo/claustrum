//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
)

// openNonBlocking adds nothing on Windows, which has no FIFO in the file system.
const openNonBlocking = 0

// nonDirGitDirIsNoRepo: on Windows a git directory that exists as a regular file is
// judged like a directory with nothing in it. A linked-worktree entry replaced by a file
// is therefore refused as an entry without commondir. A GIT_DIR naming a `.git` file is
// trusted and pinned to itself, so git answers "not a git repository" for the entry the
// file names. Measured against f6010b97 on a Windows 11 VM.
const nonDirGitDirIsNoRepo = false

// sameGitDirPath compares two lexically cleaned paths without regard to letter case.
// filepath.Clean already turned every forward slash into a backslash. A `\\?\` prefix
// or a path without a drive letter stays different. Measured against f6010b97 on a
// Windows 11 VM.
func sameGitDirPath(a, b string) bool {
	return strings.EqualFold(a, b)
}

// resolveGitDirLinks keeps the git directory g as it is spelled, unless a component of
// it is a symlink or a junction. filepath.EvalSymlinks also expands 8.3 short names,
// and f6010b97 does not. A `.git` file or a daemon GIT_DIR that names an entry as
// ...\GIT~1\WORKTR~1\<name> is therefore not an entry, because its parent is not named
// "worktrees". Its commondir then gets M1, which names the short path. Measured side
// by side against f6010b97 on a Windows 11 VM (rows K04 and K10). A path that has
// both a link and a short name is not measured. The kept path is cleaned, so the
// forward slashes that git writes in a .git file become backslashes, as in the
// f6010b97 texts (rows C14 and D02). Cleaning keeps short names.
func resolveGitDirLinks(g string) string {
	if !hasLinkComponent(g) {
		return filepath.Clean(g)
	}
	if r, err := filepath.EvalSymlinks(g); err == nil {
		return r
	}
	return g
}

// hasLinkComponent reports whether p or one of its parents is a symlink or a junction.
// Go reports a junction as ModeIrregular.
func hasLinkComponent(p string) bool {
	for cur := filepath.Clean(p); ; {
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}
