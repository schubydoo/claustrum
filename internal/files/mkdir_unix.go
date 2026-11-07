//go:build unix

package files

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// mkdirHandler is files.mkdir: Linux and macOS serve it (c9125eb8, measured).
var mkdirHandler = filesMkdir

// mkdirParams holds the members that files.mkdir reads. An extra member is
// ignored (c9125eb8, measured). A mode member is claustrum's own, not measured.
// ExpandPaths does nothing, so a "~" parent is not absolute and gets
// parent_refused (claustrum's own, not measured).
type mkdirParams struct {
	Parent string `json:"parent"`
	Name   string `json:"name"`
}

func (p *mkdirParams) ExpandPaths() {}

// mkdirParentRefused reports a parent that is empty, relative, holds a NUL, or is
// not clean: a "." or ".." component, "//" or a trailing slash (c9125eb8,
// measured on Linux; a relative parent and a ".." on macOS too). A "." component
// inside an absolute path is claustrum's own, not measured. A parent with U+FFFD is refused
// (c9125eb8, measured on Linux and macOS). The JSON decoder turns a byte that is
// not UTF-8 into U+FFFD (from the code).
func mkdirParentRefused(p string) bool {
	return !filepath.IsAbs(p) || strings.ContainsRune(p, 0) || strings.ContainsRune(p, utf8.RuneError) ||
		filepath.Clean(p) != p
}

// mkdirNameRefused reports a name that is empty, holds a slash, a backslash, a
// control character, U+2028 to U+202E or U+FFFD, or ends with a space or a dot.
// So ".", ".." and "..." are refused, and a space at the start is allowed. The
// rows tested TAB, DEL, U+0085, U+2028, U+202E, U+FFFD and the byte 0xff
// (c9125eb8, measured on Linux and macOS). The other controls and U+2029 to
// U+202D are claustrum's own, not measured.
func mkdirNameRefused(n string) bool {
	if n == "" || n[len(n)-1] == ' ' || n[len(n)-1] == '.' {
		return true
	}
	for _, r := range n {
		if r == '/' || r == '\\' || r == utf8.RuneError || unicode.IsControl(r) || (r >= 0x2028 && r <= 0x202E) {
			return true
		}
	}
	return false
}

// filesMkdir answers files.mkdir. It makes at most one folder, with mode 0777
// under the umask, and it deletes nothing. It logs one line per answered call.
func filesMkdir(req *rpc.Request) rpc.Response {
	var p mkdirParams
	if bad := rpc.Bind(req, &p); bad != nil {
		return *bad
	}
	res := mkdirAt(p.Parent, p.Name)
	if res.Errno != 0 {
		en := unix.Errno(res.Errno)
		logging.Infof("[Files] mkdir: %s (error %d: %s)", res.Code, res.Errno, en.Error())
	} else {
		logging.Infof("[Files] mkdir: %s", res.Code)
	}
	return rpc.OKResult(req.ID, res)
}

// mkdirAt runs the checks and the mkdir.
func mkdirAt(parent, name string) rpc.MkdirResult {
	if mkdirParentRefused(parent) {
		return rpc.MkdirResult{Code: "parent_refused"}
	}
	if mkdirNameRefused(name) {
		return rpc.MkdirResult{Code: "name_refused"}
	}
	// The parent opens with its links followed. Then the real path of the open
	// handle must be the parent itself, so a link at or above the parent gives
	// parent_elsewhere and makes nothing (c9125eb8, measured on Linux and macOS).
	// The mkdir goes through that handle, so a parent that is swapped after the
	// open cannot move the new folder (claustrum's own, not measured).
	fd, err := openMkdirParent(parent)
	if err != nil {
		return mkdirParentFailure(err)
	}
	defer unix.Close(fd)
	realPath, err := mkdirParentPath(fd)
	if err != nil {
		return rpc.MkdirResult{Code: "parent_unverified", Errno: int(mkdirErrno(err))}
	}
	if realPath != parent {
		return rpc.MkdirResult{Code: "parent_elsewhere"}
	}
	err = mkdirAtFd(fd, name, 0o777)
	if err == nil {
		return rpc.MkdirResult{Code: "created"}
	}
	if errors.Is(err, unix.EEXIST) {
		return rpc.MkdirResult{Code: mkdirExistsCode(fd, name)}
	}
	en := mkdirErrno(err)
	return rpc.MkdirResult{Code: mkdirErrnoCode(en), Errno: int(en)}
}

// mkdirErrnoCode gives the code of an errno from the mkdir of the name
// (c9125eb8, measured on Linux, and EACCES, ENAMETOOLONG and EROFS on macOS). Any
// errno not in the list gives failed (EIO measured on Linux, the others
// claustrum's own, not measured).
func mkdirErrnoCode(en unix.Errno) string {
	switch en {
	case unix.ENAMETOOLONG:
		return "name_too_long"
	case unix.EACCES:
		return "denied"
	case unix.EPERM:
		return "not_permitted"
	case unix.EROFS:
		return "read_only_fs"
	case unix.ENOSPC:
		return "no_space"
	case unix.EDQUOT:
		return "over_quota"
	case unix.EINVAL, unix.EILSEQ:
		return "name_unsupported"
	case unix.ENOENT:
		return "parent_removed"
	}
	return "failed"
}

// mkdirAtFd is the mkdir call. A test swaps it to give an errno that no fixture
// on Linux makes.
var mkdirAtFd = unix.Mkdirat

// mkdirParentPath reads the real path of the open parent. A test swaps it to
// make the read fail.
var mkdirParentPath = openParentPath

// openMkdirParent opens the parent folder with its links followed. An EINTR
// gets another try.
func openMkdirParent(parent string) (int, error) {
	for {
		fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}

// mkdirParentFailure gives the code of a failed parent open. EACCES is
// parent_denied, and ENOENT and ENOTDIR are parent_gone, also through a link
// (c9125eb8, measured on Linux and macOS). Any other errno, such as ELOOP, gives
// failed (claustrum's own, not measured).
func mkdirParentFailure(err error) rpc.MkdirResult {
	en := mkdirErrno(err)
	switch en {
	case unix.EACCES:
		return rpc.MkdirResult{Code: "parent_denied", Errno: int(en)}
	case unix.ENOENT, unix.ENOTDIR:
		return rpc.MkdirResult{Code: "parent_gone", Errno: int(en)}
	}
	return rpc.MkdirResult{Code: "failed", Errno: int(en)}
}

// mkdirExistsCode names what holds the name, with no link followed: a link is
// exists_link, a folder exists_folder and any other kind exists_other (c9125eb8,
// measured on Linux and macOS; a dangling link and a link to a file measured on
// Linux only). A name that went before the stat is
// exists_other (claustrum's own, not measured).
func mkdirExistsCode(fd int, name string) string {
	var st unix.Stat_t
	// A failed stat leaves Mode 0, which gives exists_other.
	_ = unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return "exists_link"
	case unix.S_IFDIR:
		return "exists_folder"
	}
	return "exists_other"
}

// mkdirErrno takes the errno out of err. An error with no errno counts as EIO.
func mkdirErrno(err error) unix.Errno {
	var en unix.Errno
	if errors.As(err, &en) && en != 0 {
		return en
	}
	return unix.EIO
}
