//go:build windows

package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/schubydoo/claustrum/internal/pathcanon"
)

// errnoCantResolve is error 1921, the answer of a link loop or a chain over the
// limit (build 683cc6a9, Windows).
const errnoCantResolve syscall.Errno = 1921

func isLinkLoopErrno(en syscall.Errno) bool { return en == errnoCantResolve }

const realpathNeedDrive = "files.realpath: the path must begin with a drive letter, a colon and a slash"

// realpathPathRefusal returns the -32602 text for a path that does not begin
// with a drive letter, a colon and a slash, or that has a component ending in a
// dot or a space. A component "." or ".." passes (claustrum's own, not measured).
func realpathPathRefusal(path string) string {
	if len(path) < 3 || !pathcanon.IsDriveLetter(path[0]) || path[1] != ':' || (path[2] != '\\' && path[2] != '/') {
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

// realpathClean cleans the text as Win32 does before it opens a path: a ".." after
// a link removes the link name and not a folder of its target. Claustrum's own,
// not measured.
func realpathClean(p string) string { return filepath.Clean(p) }

// realpathEndsInSep is false on Windows: a trailing separator is cleaned as text,
// and a file with one keeps its real name (claustrum's own, not measured).
func realpathEndsInSep(string) bool { return false }

// maxEvalLinks is the most links that realpathEval follows, as in
// filepath.EvalSymlinks (claustrum's own).
const maxEvalLinks = 255

// realpathEval resolves every symbolic link of the clean absolute path p, one
// name at a time. The target of a link joins the rest of the path and is
// cleaned as text before the next name is followed. So a ".." inside a target
// removes the name before it, also a link (build 683cc6a9, Windows). A link
// whose target is on a mapped network drive or is a `\\?\UNC\` path gives
// errNoUsableName (build 683cc6a9, Windows). Any other UNC target, and a path
// on a network drive with no such link, are claustrum's own, not measured.
// A junction to a drive path is followed as a link (4c111e86 and c9125eb8,
// Windows). A junction to a volume name acts as a folder (claustrum's own, not
// measured). The result gets the letter case of each name on disk from
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
			junction := fi.Mode()&fs.ModeIrregular != 0 && junctionToDrive(at)
			if fi.Mode()&fs.ModeSymlink == 0 && !junction {
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
			if pathcanon.OnNetwork(target) {
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

// junctionToDrive reports whether the entry at p is a junction whose target
// begins with a drive letter, a colon and a slash. os.Readlink reads a symbolic
// link or a junction and fails for any other kind of entry.
func junctionToDrive(p string) bool {
	t, err := os.Readlink(p)
	return err == nil && len(t) >= 3 && pathcanon.IsDriveLetter(t[0]) && t[1] == ':' && os.IsPathSeparator(t[2])
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
