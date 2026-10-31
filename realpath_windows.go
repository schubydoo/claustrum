//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// errnoCantResolve is error 1921, the answer of a link loop or a chain over the
// limit (build 683cc6a9, Windows VM).
const errnoCantResolve syscall.Errno = 1921

func isLinkLoopErrno(en syscall.Errno) bool { return en == errnoCantResolve }

const realpathNeedDrive = "files.realpath: the path must begin with a drive letter, a colon and a slash"

// realpathPathRefusal returns the -32602 text for a path that does not begin
// with a drive letter, a colon and a slash, or that has a component ending in a
// dot or a space. A component "." or ".." passes (claustrum's own, not measured).
func realpathPathRefusal(path string) string {
	if len(path) < 3 || !isDriveLetter(path[0]) || path[1] != ':' || (path[2] != '\\' && path[2] != '/') {
		return realpathNeedDrive
	}
	comps := strings.FieldsFunc(path[3:], func(r rune) bool { return r == '\\' || r == '/' })
	for _, comp := range comps {
		if comp == "." || comp == ".." {
			continue
		}
		if last := comp[len(comp)-1]; last == '.' || last == ' ' {
			return "files.realpath: a path component ends in a dot or a space"
		}
	}
	return ""
}

func isDriveLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// realpathClean cleans the text as Win32 does before it opens a path: a ".." after
// a link removes the link name and not a folder of its target. Claustrum's own,
// not measured. A ".." inside a link target is resolved after the next link, not
// by text (claustrum's own, not covered).
func realpathClean(p string) string { return filepath.Clean(p) }

// realpathJoin reads a relative link target from the folder of the link. A target
// with a root and no drive, such as `\gone`, takes the drive of the folder
// (claustrum's own, not measured).
func realpathJoin(dir, target string) string {
	if target != "" && os.IsPathSeparator(target[0]) {
		return filepath.VolumeName(dir) + target
	}
	return dir + `\` + target
}
