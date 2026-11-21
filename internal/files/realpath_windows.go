//go:build windows

package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/schubydoo/claustrum/internal/pathcanon"
)

// errnoCantResolve is error 1921, the answer of a link loop or a chain over the
// limit (build 683cc6a9, Windows).
const errnoCantResolve syscall.Errno = 1921

func isLinkLoopErrno(en syscall.Errno) bool { return en == errnoCantResolve }

const realpathNeedDrive = "files.realpath: the path must begin with a drive letter, a colon and a slash"

// realpathStreamText is the -32602 text for a path with a colon after the drive
// letter, as `f.txt:stream` or `f.txt:name:$DATA` (4c111e86, Windows).
const realpathStreamText = "files.realpath: the path has a colon after the drive letter's (alternate data streams are not supported)"

// realpathPathRefusal returns the -32602 text for a path that does not begin
// with a drive letter, a colon and a slash, that has a colon after the drive
// letter (4c111e86, Windows), or that has a component ending in a dot or a
// space. The measured colon rows are `f.txt:stream` and `f.txt:nope:$DATA`, a
// missing stream of a file that exists. Claustrum's own, not measured: a colon in
// a folder name, a stream that exists, a passing component "." or "..", and the
// colon test before the component test.
func realpathPathRefusal(path string) string {
	if len(path) < 3 || !pathcanon.IsDriveLetter(path[0]) || path[1] != ':' || (path[2] != '\\' && path[2] != '/') {
		return realpathNeedDrive
	}
	if strings.ContainsRune(path[2:], ':') {
		return realpathStreamText
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
// a link removes the link name and not a folder of its target (claustrum's own,
// not measured). A path on a SUBST drive then names the folder of that drive
// (substReal).
func realpathClean(p string) string { return substReal(filepath.Clean(p)) }

// realpathEndsInSep is false on Windows: a trailing separator is cleaned as text,
// and a file with one keeps its real name (claustrum's own, not measured).
func realpathEndsInSep(string) bool { return false }

// substTarget gives the folder that the SUBST drive of drive (as `X:`) names, as
// QueryDosDevice gives it, or false for a drive that is no SUBST drive. It is a
// seam for the tests.
var substTarget = func(drive string) (string, bool) {
	name, err := windows.UTF16PtrFromString(drive)
	if err != nil {
		return "", false
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.QueryDosDevice(name, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		return "", false
	}
	t := windows.UTF16ToString(buf[:n])
	if !strings.HasPrefix(t, `\??\`) {
		return "", false
	}
	return t[len(`\??\`):], true
}

// substReal puts the folder of a SUBST drive in place of the drive letter of the
// clean path p: a path on a SUBST drive resolves to its real path on the drive
// below (4c111e86, Windows). A SUBST drive of a SUBST drive is followed up to 26
// times, and a SUBST folder on a share takes the `\\` form, so realpathEval
// gives errNoUsableName (claustrum's own, not measured).
func substReal(p string) string {
	for range 26 {
		if len(p) < 2 || !pathcanon.IsDriveLetter(p[0]) || p[1] != ':' {
			return p
		}
		t, ok := substTarget(strings.ToUpper(p[:2]))
		if !ok {
			return p
		}
		if len(t) >= 4 && strings.EqualFold(t[:4], `UNC\`) {
			t = `\` + t[3:]
		}
		p = filepath.Clean(t + `\` + p[2:])
	}
	return p
}

// maxEvalLinks is the most links that realpathEval follows. A chain of 31
// junctions resolves, and 32, 33 and 34 give error 1921 (4c111e86, Windows). The
// same count for symbolic links and for links in more than one name is
// claustrum's own, not measured.
const maxEvalLinks = 31

// realpathEval resolves every symbolic link of the clean absolute path p, one
// name at a time. The target of a link joins the rest of the path and is
// cleaned as text before the next name is followed. So a ".." inside a target
// removes the name before it, also a link (build 683cc6a9, Windows). A path on a
// mapped network drive, also its root, gives errNoUsableName, and so does a link
// whose target is on one or is a `\\host\share`, `\\?\UNC\` or `\\.\UNC\` path
// (683cc6a9 and 4c111e86, Windows): the target becomes the next path, and the
// test at the top of the loop answers it. Other network forms are claustrum's
// own, not measured. A path or link target on a SUBST drive goes on from its
// real path (4c111e86, Windows).
// A junction to a drive path is followed as a link (4c111e86 and c9125eb8,
// Windows). A junction to a volume name acts as a folder (claustrum's own, not
// measured). The result gets the letter case of each name on disk from
// filepath.EvalSymlinks.
func realpathEval(p string) (string, error) {
	p = filepath.Clean(p)
	for links := 0; ; {
		p = substReal(p)
		if pathcanon.OnNetwork(p) {
			return "", errNoUsableName
		}
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
				return "", errnoCantResolve
			}
			target, err := os.Readlink(at)
			if err != nil {
				return "", err
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

// realpathMissingFailure is the answer for the missing clean path: the
// failure of the nearest existing folder above it, when that folder gives
// errNoUsableName or error 1921, or nil. So a missing name on a mapped network
// drive, behind a link to a network place or behind a chain of 32 junctions
// fails as the folder does, with and without missingLeaf (4c111e86, Windows).
// Behind a network link it does so with missingLeaf also when the folder between
// is missing (4c111e86, Windows); the other cases of a missing folder between,
// the walk up by text and nil for other failures of the folder are claustrum's
// own, not measured.
func realpathMissingFailure(path string) error {
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil {
			_, err = realpathEval(p)
			if errors.Is(err, errNoUsableName) || errors.Is(err, errnoCantResolve) {
				return err
			}
			return nil
		}
		if filepath.Dir(p) == p {
			return nil
		}
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
