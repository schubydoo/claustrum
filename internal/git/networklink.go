package git

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// The test of a repository path that goes through a symbolic link or a junction to
// a network or device path. claustrum runs it on Windows only, before any git call
// on the repository, for the baseRepo of git.status and git.worktree_create and the
// path of git.info (measured on Windows). The test reads the entries of the path
// only, and it starts no git.

// networkLinkChecks turns the test on. It is true on Windows. The tests set it on
// every system.
var networkLinkChecks = gitEnvWindowsForm

// readLinkTarget returns the target of the symbolic link or the junction at path,
// as its reparse data holds it, and true. It returns false for any other entry
// and for a read that fails (linkTarget). It is a seam for the tests.
var readLinkTarget = linkTarget

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
// The test of unlookablePathRefusal comes first (claustrum's own, not measured).
func networkLinkRefusal(path string) string {
	if msg := unlookablePathRefusal(path); msg != "" {
		return msg
	}
	if networkLinkChecks && throughNetworkLink(path) {
		return networkLinkText(path)
	}
	return ""
}

// lstatPath is os.Lstat. It is a seam for the tests of unlookablePathRefusal.
var lstatPath = os.Lstat

// unlookableShareText and longUNCText name the path cleaned, as networkLinkText
// does. The rows send clean paths, so the cleaning is claustrum's own, not measured.
func unlookableShareText(path string) string {
	return "Claude couldn't look at `" + filepath.Clean(path) + "` to check it for links, so git wasn't run. Check that the network share is reachable and that your account can open that folder, then try again."
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
// claustrum's own, not measured: other \\?\ and \\.\ forms pass, and so does
// the share root alone. A forward slash counts as a separator. Any failed look
// other than a missing entry refuses. Go reads a host that cannot be reached
// (ERROR_BAD_NETPATH) as a missing entry, so that path passes.
func unlookablePathRefusal(path string) string {
	if !networkLinkChecks || len(path) < 4 || !isSlash(path[0]) || !isSlash(path[1]) {
		return ""
	}
	if (path[2] == '?' || path[2] == '.') && isSlash(path[3]) {
		if path[2] == '?' && len(path) >= 8 && strings.EqualFold(path[4:7], "UNC") && isSlash(path[7]) {
			return longUNCText(path)
		}
		return ""
	}
	if len(strings.FieldsFunc(path, func(r rune) bool { return r < 0x80 && isSlash(byte(r)) })) < 3 {
		return ""
	}
	if _, err := lstatPath(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return unlookableShareText(path)
	}
	return ""
}

// throughNetworkLink reports whether a name of the absolute path is a link whose
// target leaves the local drives. The names are tested from the volume down, so
// a name below a link is read through that link by the system. The target of a
// local link is not followed further, and a name that cannot be read ends the
// test with false (claustrum's own, not measured).
func throughNetworkLink(path string) bool {
	p, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	vol := filepath.VolumeName(p)
	cur := vol + string(filepath.Separator)
	for _, name := range strings.FieldsFunc(p[len(vol):], isPathSep) {
		cur = filepath.Join(cur, name)
		fi, err := os.Lstat(cur)
		if err != nil {
			return false
		}
		if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) == 0 {
			continue
		}
		if target, ok := readLinkTarget(cur); ok && targetLeavesLocalDrive(target) {
			return true
		}
	}
	return false
}

// targetLeavesLocalDrive reports whether a link target names something other than
// a path on a drive letter. A target in the NT form \??\ or the form \\?\ is local
// only with a drive letter after the prefix, so UNC\, a device name, Volume{guid}
// and GLOBALROOT are not. A target that starts with two separators is a UNC path
// or a \\.\ device path. A relative target stays on the drive of its link. The
// rows measure \??\UNC\, \??\NUL, \??\Volume{guid} and \??\GLOBALROOT. The other
// forms are claustrum's own, not measured.
func targetLeavesLocalDrive(target string) bool {
	for _, prefix := range []string{`\??\`, `\\?\`} {
		if strings.HasPrefix(target, prefix) {
			return !startsWithDrive(target[len(prefix):])
		}
	}
	return len(target) >= 2 && isSlash(target[0]) && isSlash(target[1])
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
