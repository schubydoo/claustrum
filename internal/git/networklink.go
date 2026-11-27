package git

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/schubydoo/claustrum/internal/logging"
)

// The test of a repository path that goes through a symbolic link or a junction to
// a network or device path. claustrum runs it on Windows only, before any git call
// on the repository, for the baseRepo of git.status, git.worktree_create and
// git.worktree_remove and the path of git.info and git.list_branches (measured on Windows). The test reads the entries of the path
// only, and it starts no git.

// networkLinkChecks turns the test on. It is true on Windows. The tests set it on
// every system.
var networkLinkChecks = gitEnvWindowsForm

// readLinkTarget returns the target of the symbolic link or the junction at path,
// as its reparse data holds it, and true. It returns false for any other entry
// and for a read that fails (linkTarget). It is a seam for the tests.
var readLinkTarget = linkTarget

// readLinkRelative reports whether the symbolic link at path has the relative flag
// in its reparse data (linkRelative). It is a seam for the tests.
var readLinkRelative = linkRelative

// networkLinkText names the path cleaned. The rows send clean paths with
// backslashes, so the cleaning is claustrum's own, not measured.
func networkLinkText(path string) string {
	return "Claude doesn't follow `" + filepath.Clean(path) + "` on a Windows host: it goes through a symlink or junction to a network or device path, so git wasn't run. Replace that link with a folder on a local drive. Or open the folder by its network path."
}

// networkLinkRefusal is the refusal of a repository path that goes through a link
// to a network or device path, or "". b3081b51 refuses a link to a UNC path,
// to \\.\NUL, to a \\?\Volume{guid} path and to a
// \\?\GLOBALROOT path, also when the link leads to a local folder. A SUBST
// drive, a mapped drive and a UNC path with no link on a share that answers
// pass (measured on Windows).
// The tests of unlookablePathRefusal and pathLookRefusal come first (claustrum's
// own, not measured).
func networkLinkRefusal(path string) string {
	msg, _ := networkLinkRefusalAt(path)
	return msg
}

// networkLinkRefusalAt is networkLinkRefusal. lastJunction is true when the
// refusal is shareLinkText for a junction that is the last name of path. It
// takes the kind and the place from the looks of the walk, with no other look.
func networkLinkRefusalAt(path string) (msg string, lastJunction bool) {
	if msg, lastJunction := unlookableRefusalAt(path); msg != "" {
		return msg, lastJunction
	}
	if msg := pathLookRefusal(path); msg != "" {
		return msg, false
	}
	return linkHopRefusal(path), false
}

// lstatPath is os.Lstat. It is a seam for the tests of unlookablePathRefusal and
// shareGitDir.
var lstatPath = os.Lstat

// unlookableShareText names the folder it is given, cleaned. longUNCText names
// the path cleaned, as networkLinkText does. The rows send clean paths, so the
// cleaning is claustrum's own, not measured.
func unlookableShareText(path string) string {
	return "Claude couldn't look at `" + filepath.Clean(path) + "` to check it for links, so git wasn't run. Check that the network share is reachable and that your account can open that folder, then try again."
}

// deniedShareText names the path and the folder whose look was denied, both
// cleaned (firstFailedFolder, 4c111e86, measured on Windows).
func deniedShareText(path, folder string) string {
	return "Claude couldn't check `" + filepath.Clean(path) + "` for links because Windows didn't let your account read `" + filepath.Clean(folder) + "`, so git wasn't run. Ask for read access to that folder."
}

// firstFailedFolder returns the first folder of the share path, from the first
// name below the share root down, whose look fails, and the error of that look.
// When no folder above the last name fails, it returns path and err, the error
// of the look at path. So the texts name `\\host\share\sub` for a share that
// denies every account, the path itself for a path one name below the root of
// a share that denies the account, and `\\host\share\a` for a deeper path on a
// share that does not exist, and the log line quotes the error of that folder
// (4c111e86, measured on Windows where every look on the share fails). The prefixes keep
// the spelling of the request. A folder that passes above the first one that
// fails is claustrum's own, not measured.
func firstFailedFolder(path string, err error) (string, error) {
	seps := 0
	for i := 1; i < len(path); i++ {
		if !isSlash(path[i]) || isSlash(path[i-1]) {
			continue
		}
		if seps++; seps < 3 {
			continue
		}
		if _, ferr := lstatPath(path[:i]); ferr != nil {
			return path[:i], ferr
		}
	}
	return path, err
}

// shareLinkText names the link on the share.
func shareLinkText(link string) string {
	return "Claude doesn't follow links on a network share, and `" + link + "` is a symlink or junction, so git wasn't run. Open the folder it points to instead, or replace the link with a real folder."
}

func longUNCText(path string) string {
	return "Claude can't check where `" + filepath.Clean(path) + "` leads because it doesn't start with a drive letter (C:\\...) or a share (\\\\server\\share\\...), so git wasn't run. Open the folder again using a path that does."
}

// unlookablePathRefusal is the refusal of a repository path that the link test
// cannot look at, or "". It runs only with networkLinkChecks. b3081b51 refuses a
// \\?\UNC\ path, and a path on a share that does not exist on its host,
// on git.info, git.status, git.list_branches,
// git.worktree_create and git.worktree_remove (measured on Windows). A missing
// folder on a share and a path on a missing drive pass (measured on Windows).
//
// A failed look names the first folder whose look failed (firstFailedFolder).
// 4c111e86 logs one "looking for links" line for a share that does not exist.
// It gives deniedShareText for a share that denies every account. It refuses a
// directory symbolic link on a share that answers with shareLinkText. A host
// name that does not resolve passes (measured on Windows, all five methods).
//
// claustrum's own, not measured: other \\?\ and \\.\ forms pass, and so does
// the share root alone. A forward slash counts as a separator. Any failed look
// other than a missing entry or a denied read refuses and logs the line. Go reads
// a host that cannot be reached (ERROR_BAD_NETPATH) as a missing entry, so that
// path passes. A junction counts as a link, and so does a link above the last
// name. A path that is missing below a link passes.
func unlookablePathRefusal(path string) string {
	msg, _ := unlookableRefusalAt(path)
	return msg
}

// unlookableRefusalAt is unlookablePathRefusal, with lastJunction as in
// networkLinkRefusalAt.
func unlookableRefusalAt(path string) (msg string, lastJunction bool) {
	if !networkLinkChecks || len(path) < 4 || !isSlash(path[0]) || !isSlash(path[1]) {
		return "", false
	}
	if (path[2] == '?' || path[2] == '.') && isSlash(path[3]) {
		if path[2] == '?' && len(path) >= 8 && strings.EqualFold(path[4:7], "UNC") && isSlash(path[7]) {
			return longUNCText(path), false
		}
		return "", false
	}
	if len(strings.FieldsFunc(path, func(r rune) bool { return r < 0x80 && isSlash(byte(r)) })) < 3 {
		return "", false
	}
	_, err := lstatPath(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", false
	case errors.Is(err, fs.ErrPermission) || networkAccessDenied(err):
		folder, _ := firstFailedFolder(path, err)
		return deniedShareText(path, folder), false
	case err != nil:
		folder, ferr := firstFailedFolder(path, err)
		logging.Infof("[GitHandler] looking for links in %q: %q", path, ferr.Error())
		return unlookableShareText(folder), false
	}
	if link, lastJunction := linkOnShare(path); link != "" {
		return shareLinkText(link), lastJunction
	}
	return "", false
}

// linkOnShare returns the first name below the share of the UNC path that is a
// link (folderIsLink), as a path with backslashes, or "". A name that cannot be
// read ends the walk with "" (claustrum's own, not measured). lastJunction is
// true when that link is a junction (no symbolic link bit) and the last name.
func linkOnShare(path string) (link string, lastJunction bool) {
	names := strings.FieldsFunc(path, func(r rune) bool { return r < 0x80 && isSlash(byte(r)) })
	cur := `\\` + names[0] + `\` + names[1]
	for i, name := range names[2:] {
		cur += `\` + name
		fi, err := lstatPath(cur)
		if err != nil {
			return "", false
		}
		if m := fi.Mode(); folderIsLink(m) {
			return cur, m&fs.ModeSymlink == 0 && i == len(names)-3
		}
	}
	return "", false
}

// unreadableLinkText names the path cleaned, as networkLinkText does (4c111e86 and
// 87375cc7, measured on Windows).
func unreadableLinkText(path string) string {
	return uncheckedLinkText(path, "a link that Claude can't read")
}

// rootedLinkText is the text of a directory symbolic link whose relative target
// starts with one backslash (4c111e86 and 87375cc7, measured on Windows).
func rootedLinkText(path string) string {
	return uncheckedLinkText(path, "a link target starts with a single backslash (the volume root)")
}

func uncheckedLinkText(path, reason string) string {
	return "Claude couldn't check that `" + filepath.Clean(path) + "` stays on a local drive (" + reason + "), so git wasn't run. If a folder name in that path ends in a dot or a space, rename it. Otherwise, replace the link in that path with the folder it points to."
}

// linkHopRefusal walks each name of path and each hop of each link on it, and returns
// the refusal of the first hop that does not stay on a local drive, or "". 4c111e86
// and 87375cc7 refuse the last hop of a chain, a link in the middle of the path and a
// chain that ends in a UNC path (measured on Windows). A chain of linkChainCap hops
// gets linkChainText (claustrum's own for a junction, not measured). A missing name
// ends the walk with no refusal, as a dead local link does (measured). A name that
// the Lstat cannot read ends it too (claustrum's own, not measured).
func linkHopRefusal(path string) string {
	if !networkLinkChecks {
		return ""
	}
	p, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	for hops := 0; ; hops++ {
		if hops >= linkChainCap {
			return linkChainText(path)
		}
		next, verdict := firstLinkHop(p)
		switch verdict {
		case hopToDevice:
			return networkLinkText(path)
		case hopUnreadable:
			return unreadableLinkText(path)
		case hopRooted:
			return rootedLinkText(path)
		}
		if next == "" {
			return ""
		}
		p = next
	}
}

// firstLinkHop finds the first link on the absolute path p and returns p with that
// link replaced by its target. next is "" when p has no link.
func firstLinkHop(p string) (next string, verdict linkHop) {
	vol := filepath.VolumeName(p)
	cur := vol + string(filepath.Separator)
	names := strings.FieldsFunc(p[len(vol):], isPathSep)
	for i, name := range names {
		at := filepath.Join(cur, name)
		fi, err := os.Lstat(at)
		if err != nil {
			return "", hopFollows
		}
		if !folderIsLink(fi.Mode()) {
			cur = at
			continue
		}
		target, ok := readLinkTarget(at)
		if !ok {
			return "", hopUnreadable
		}
		relative := readLinkRelative(at)
		to, verdict := judgeLinkTarget(target, relative)
		if verdict != hopFollows {
			return "", verdict
		}
		if relative {
			to = filepath.Join(cur, to)
		}
		return filepath.Join(append([]string{to}, names[i+1:]...)...), hopFollows
	}
	return "", hopFollows
}

// linkHop is the verdict on one target of a link.
type linkHop int

const (
	hopFollows linkHop = iota
	hopToDevice
	hopUnreadable
	hopRooted
)

// gitFileLinkText is the text of 87375cc7 (measured on Windows). It names the
// `.git` file cleaned, as networkLinkText names its path (claustrum's own, not measured).
func gitFileLinkText(dotGit string) string {
	return "Claude doesn't follow the path in `" + filepath.Clean(dotGit) + "` on a Windows host: it goes through a symlink or junction to a network or device path, so git wasn't run. Replace that link with a folder on a local drive."
}

// gitFileLinkRefusal is the refusal of a request on dir whose walk (findGitDir)
// finds a `.git` file that names a path through a link to a network or device
// path (hopsToDevice), or "". 87375cc7 refuses git.info, git.status,
// git.list_branches, git.worktree_create and git.worktree_remove so, with no git
// call on the repository, for a junction to `\??\UNC\...` and for a directory
// symbolic link to `\\localhost\C$\...` (measured on Windows). With
// "shadowDirOff":true it gives no such text (measured), so the callers skip it
// then. claustrum's own, not measured: a link that cannot be read, a rooted target
// and a long chain pass here. A daemon GIT_DIR and the state fallback-git keep the
// test (claustrum's own, not measured).
func gitFileLinkRefusal(dir string) string {
	if !networkLinkChecks {
		return ""
	}
	if dir == "" {
		dir = "."
	}
	// A path that is not a folder gets no walk, so this test makes no resolve
	// there (resolveWalkLinks). The trust check judges it (from the code).
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	start, err := walkStart(dir)
	if err != nil {
		return ""
	}
	if start, err = filepath.Abs(start); err != nil {
		return ""
	}
	g, holder, ok := findGitDir(start)
	if !ok || holder == "" {
		return ""
	}
	dotGit := filepath.Join(holder, ".git")
	if fi, err := os.Stat(dotGit); err != nil || !fi.Mode().IsRegular() || !hopsToDevice(g) {
		return ""
	}
	return gitFileLinkText(dotGit)
}

// hopsToDevice reports whether the walk of linkHopRefusal on path meets a hop to
// a network or device path (hopToDevice) before linkChainCap hops.
func hopsToDevice(path string) bool {
	p, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for hops := 0; hops < linkChainCap; hops++ {
		next, verdict := firstLinkHop(p)
		if verdict != hopFollows || next == "" {
			return verdict == hopToDevice
		}
		p = next
	}
	return false
}

// judgeLinkTarget judges the substitute name target of a link. relative is the
// relative flag of a symbolic link. It returns the path that the link leads to when
// the walk follows it. 4c111e86 and 87375cc7 answer so on Windows (measured):
//
//   - `\??\<drive>:\...` follows. A colon after the drive gets the device refusal (claustrum's own, not measured).
//   - `\??\` before anything else, such as `Volume{guid}`, `UNC\`, `NUL` or
//     `GLOBALROOT`, gets the device refusal.
//   - Any other absolute name, such as `\\?\C:\`, `\Device\`, `\GLOBAL??\`,
//     `\DosDevices\`, `C:\` or a rooted `\path`, cannot be read.
//   - A relative name rooted at `\` gets rootedLinkText. With a colon it gets the
//     device refusal.
//
// A relative name with no root follows, and one with a colon cannot be read. An empty
// name cannot be read (all three claustrum's own, not measured).
func judgeLinkTarget(target string, relative bool) (string, linkHop) {
	if target == "" {
		return "", hopUnreadable
	}
	if relative {
		switch {
		case isSlash(target[0]) && strings.Contains(target, ":"):
			return "", hopToDevice
		case isSlash(target[0]):
			return "", hopRooted
		case strings.Contains(target, ":"):
			return "", hopUnreadable
		}
		return target, hopFollows
	}
	rest, ok := strings.CutPrefix(target, `\??\`)
	if !ok {
		return "", hopUnreadable
	}
	if !startsWithDrive(rest) || strings.Contains(rest[2:], ":") {
		return "", hopToDevice
	}
	return rest, hopFollows
}

// startsWithDrive reports whether s is a drive letter and a colon, alone or
// before a separator.
func startsWithDrive(s string) bool {
	if len(s) < 2 || s[1] != ':' {
		return false
	}
	c := s[0] | 0x20
	return c >= 'a' && c <= 'z' && (len(s) == 2 || isSlash(s[2]))
}

func isSlash(c byte) bool { return c == '\\' || c == '/' }

// The tags of the two kinds of link in the reparse data of Windows.
const (
	reparseTagMountPoint = 0xA0000003
	reparseTagSymlink    = 0xA000000C
)

// symlinkFlagRelative is the relative flag in the reparse data of a symbolic link.
const symlinkFlagRelative = 1

// reparseIsRelative reports whether data is that of a symbolic link with the
// relative flag.
func reparseIsRelative(data []byte) bool {
	return len(data) >= 20 && binary.LittleEndian.Uint32(data) == reparseTagSymlink &&
		binary.LittleEndian.Uint32(data[16:])&symlinkFlagRelative != 0
}

// reparseSubstituteName returns the substitute name of the reparse data of a
// symbolic link or a junction, and true. Any other tag and data that is too
// short give false. The layout is the one of FSCTL_GET_REPARSE_POINT: the tag,
// the data length and a reserved word, then the offset and length of the
// substitute name. A symbolic link has a flags word before its names, and a
// junction has none.
func reparseSubstituteName(data []byte) (string, bool) {
	if len(data) < 16 {
		return "", false
	}
	names := 16
	switch binary.LittleEndian.Uint32(data) {
	case reparseTagMountPoint:
	case reparseTagSymlink:
		names = 20
	default:
		return "", false
	}
	off := names + int(binary.LittleEndian.Uint16(data[8:]))
	n := int(binary.LittleEndian.Uint16(data[10:]))
	if n%2 != 0 || off+n > len(data) {
		return "", false
	}
	u := make([]uint16, n/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(data[off+2*i:])
	}
	return string(utf16.Decode(u)), true
}
