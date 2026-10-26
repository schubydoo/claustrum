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
// trusted and pinned to itself.
const nonDirGitDirIsNoRepo = false

// registrationsLinkRefused is false on Windows: the rules of a registrations folder
// that is a link are those of Linux (round tier0-24), and Windows is not measured.
const registrationsLinkRefused = false

// entryLinkRefusedLate is false on Windows (round tier0-24 ran on Linux only).
const entryLinkRefusedLate = false

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
// "worktrees". Its commondir then counts as stray, and its content "../.." gets the
// stray-content text (S1 of docs/protocol/git-rules-trust.md), which names the short path. f6010b97
// refused it with its older stray text on a Windows 11 VM (rows K04 and K10). The S1
// text follows from the stray rules and is not measured on 89cb6289. A path that has
// both a link and a short name is not measured. The kept path is cleaned, so the
// forward slashes that git writes in a .git file become backslashes, as in the
// f6010b97 texts (rows C14 and D02). Cleaning keeps short names.
//
// A git directory that is a directory symbolic link is kept as it is spelled
// too (gitDirIsSymlink). The link test of the gate then finds the link at that
// path and refuses the request, as it does for a junction (gitDirLinkDetail).
// filepath.EvalSymlinks resolves such a link and no junction, so the gate got
// the target of the link, a real directory, and accepted it.
func resolveGitDirLinks(g string) string {
	if !hasLinkComponent(g) {
		return filepath.Clean(g)
	}
	if gitDirIsSymlink(g) {
		return filepath.Clean(g)
	}
	if r, err := filepath.EvalSymlinks(g); err == nil {
		return r
	}
	return g
}

// gitDirIsSymlink reports whether the git directory g is a symbolic link, or
// whether g is an entry `worktrees\<name>` of a git directory that is one.
//
// With a directory symbolic link (`mklink /D`) at `repo\.git`, 5fd08069
// refuses each git method with the text of a junction there: `this
// repository's git config cannot be used: "C:\\o\\w\\repo\\.git\\config": is,
// or is reached through, a link that Claude does not follow here; replace the
// link with what it points to`. It does so for a link that is there before
// the daemon starts and for one that is put there between two requests. A
// git.status on a linked worktree of that repository gets the same text
// (cells D-symd-start, D-symd-late and DT-symd-late-to-T of
// val-s3p2b2-12-windows, Windows VM, 2 of 2 runs each). claustrum gives equal
// replies in those cells, and the folder behind the link is unchanged
// (section 1.1 of val-s3p2b2-13-windows, Windows VM, 2 of 2 runs each).
//
// With "shadowDirOff":true in every request, both builds accept the link, and
// git works in the folder behind it (section 1.2 of that round).
//
// Not measured: a link that a daemon GIT_DIR or a `.git` file names, and a
// link in a folder above the git directory together with one at it. They get
// the same rule.
func gitDirIsSymlink(g string) bool {
	isLink := func(p string) bool {
		fi, err := os.Lstat(p)
		return err == nil && fi.Mode()&os.ModeSymlink != 0
	}
	if isLink(g) {
		return true
	}
	parent := filepath.Dir(filepath.Clean(g))
	return filepath.Base(parent) == "worktrees" && isLink(filepath.Dir(parent))
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

// gitVersionDir is the working folder of the git calls outside a repository: the
// `git version` run, the probe, the version gate, the excludes read and the read of
// config text. It is SystemRoot if that is an absolute path, else the folder of the
// executable of the daemon, else "", the working folder of the daemon
// (claustrum's own, not measured). It is read once at the start.
var gitVersionDir = func() string {
	exe, err := os.Executable()
	return windowsCallDir(os.Getenv("SystemRoot"), exe, err)
}()

// nonFolderStartRefuses is true on Windows. A configuration listing that cannot start
// in a path that exists and is not a folder is a refusal there, as on 89cb6289 (Windows
// VM, cells A-01 to A-04, A-04b2, A-06, A-10, A-14 and Wc). See unenterableDir.
var nonFolderStartRefuses = true

// walkStartsAsSpelled is true on Windows. A directory that filepath.EvalSymlinks cannot
// resolve starts the walk of the trust check and of the git.info root as it is spelled
// (walkStart). It is a variable so that a test can set it.
var walkStartsAsSpelled = true
