//go:build windows

package main

import (
	"errors"
	"io/fs"
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
// not measured.
func realpathClean(p string) string { return filepath.Clean(p) }

// maxEvalLinks is the most links that realpathEval follows, as in
// filepath.EvalSymlinks (claustrum's own).
const maxEvalLinks = 255

// realpathEval resolves every symbolic link of the clean absolute path p, one
// name at a time. The target of a link joins the rest of the path and is
// cleaned as text before the next name is followed. So a ".." inside a target
// removes the name before it, also a link (build 683cc6a9, Windows). A link
// whose target is on a mapped network drive or is a `\\?\UNC\` path gives
// errNoUsableName (build 683cc6a9, Windows). Any other UNC target, and a path
// on a network drive with no such link, are claustrum's own, not measured. A junction acts as in filepath.EvalSymlinks.
// The result gets the letter case of each name on disk from
// filepath.EvalSymlinks.
func realpathEval(p string) (string, error) {
	p = filepath.Clean(p)
	for links := 0; ; {
		vol := filepath.VolumeName(p)
		names := strings.FieldsFunc(p[len(vol):], func(r rune) bool { return r == '\\' || r == '/' })
		cur := vol + `\`
		next := ""
		for i, name := range names {
			at := filepath.Join(cur, name)
			fi, err := os.Lstat(at)
			if err != nil {
				return "", err
			}
			if fi.Mode()&fs.ModeSymlink == 0 {
				if !fi.IsDir() && i < len(names)-1 {
					return "", syscall.ENOTDIR
				}
				cur = at
				continue
			}
			if links++; links > maxEvalLinks {
				return "", errors.New("EvalSymlinks: too many links")
			}
			target, err := os.Readlink(at)
			if err != nil {
				return "", err
			}
			if onNetwork(target) {
				return "", errNoUsableName
			}
			if !filepath.IsAbs(target) {
				target = realpathJoin(cur, target)
			}
			next = filepath.Clean(target + `\` + strings.Join(names[i+1:], `\`))
			break
		}
		if next == "" {
			return filepath.EvalSymlinks(cur)
		}
		p = next
	}
}

// realpathJoin reads a relative link target from the folder of the link. A target
// with a root and no drive, such as `\gone`, takes the drive of the folder
// (claustrum's own, not measured).
func realpathJoin(dir, target string) string {
	if target != "" && os.IsPathSeparator(target[0]) {
		return filepath.VolumeName(dir) + target
	}
	return dir + `\` + target
}
