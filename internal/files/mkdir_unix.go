//go:build unix

package files

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// mkdirServed is true: Linux and macOS serve files.mkdir (c9125eb8, measured).
const mkdirServed = true

// mkdirParams holds the members that files.mkdir reads. Other members, mode too,
// are ignored (c9125eb8, measured on Linux). ExpandPaths does nothing, so a "~"
// parent is not absolute and gets parent_refused (claustrum's own, not measured).
type mkdirParams struct {
	Parent string `json:"parent"`
	Name   string `json:"name"`
}

func (p *mkdirParams) ExpandPaths() {}

// mkdirParentRefused reports a parent that is empty, relative, holds a NUL, or is
// not clean: a "." or ".." component, "//" or a trailing slash (c9125eb8,
// measured on Linux and macOS). A "." component inside an absolute path and the
// parent "/" are claustrum's own, not measured.
func mkdirParentRefused(p string) bool {
	return !filepath.IsAbs(p) || strings.ContainsRune(p, 0) || filepath.Clean(p) != p
}

// mkdirNameRefused reports a name that is empty, "." or "..", holds a slash or a
// NUL, or starts and ends with a space (c9125eb8, measured on Linux and macOS). A
// space at one end only is refused too (claustrum's own, not measured).
func mkdirNameRefused(n string) bool {
	return n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\x00") ||
		n[0] == ' ' || n[len(n)-1] == ' '
}

// filesMkdir answers files.mkdir. It makes at most one folder, with mode 0755
// under the umask, and it deletes nothing. It logs one line per answered call.
func filesMkdir(req *rpc.Request) rpc.Response {
	var p mkdirParams
	if bad := rpc.Bind(req, &p); bad != nil {
		return *bad
	}
	res, fail := mkdirAt(p.Parent, p.Name)
	if fail != 0 {
		msg := "error " + strconv.Itoa(int(fail)) + ": " + fail.Error()
		logging.Infof("[Files] mkdir: failed (%s)", msg)
		return rpc.ErrResult(req.ID, rpc.CodeInternal, "files.mkdir: "+msg)
	}
	if res.Errno != 0 {
		en := unix.Errno(res.Errno)
		logging.Infof("[Files] mkdir: %s (error %d: %s)", res.Code, res.Errno, en.Error())
	} else {
		logging.Infof("[Files] mkdir: %s", res.Code)
	}
	return rpc.OKResult(req.ID, res)
}

// mkdirAt runs the checks and the mkdir. A non-zero fail is an errno that no
// code covers. It gets -32603 (claustrum's own, not measured). The code
// parent_unverified is not built, because no row staged it.
func mkdirAt(parent, name string) (rpc.MkdirResult, unix.Errno) {
	if mkdirParentRefused(parent) {
		return rpc.MkdirResult{Code: "parent_refused"}, 0
	}
	if mkdirNameRefused(name) {
		return rpc.MkdirResult{Code: "name_refused"}, 0
	}
	// The parent opens with no link followed in its last name, and the mkdir
	// goes through that handle. A parent that is swapped for a link after the
	// open cannot move the new folder (claustrum's own, not measured). A link
	// in a folder above the parent is followed (claustrum's own, not measured).
	fd, err := openMkdirParent(parent)
	if err != nil {
		return mkdirParentFailure(parent, err)
	}
	defer unix.Close(fd)
	err = unix.Mkdirat(fd, name, 0o755)
	switch {
	case err == nil:
		return rpc.MkdirResult{Code: "created"}, 0
	case errors.Is(err, unix.EEXIST):
		return rpc.MkdirResult{Code: mkdirExistsCode(fd, name)}, 0
	case errors.Is(err, unix.ENAMETOOLONG):
		return rpc.MkdirResult{Code: "name_too_long", Errno: int(unix.ENAMETOOLONG)}, 0
	case errors.Is(err, unix.EACCES):
		return rpc.MkdirResult{Code: "denied", Errno: int(unix.EACCES)}, 0
	}
	return rpc.MkdirResult{}, mkdirErrno(err)
}

// openMkdirParent opens the parent folder. O_NOFOLLOW refuses a link as the
// last name. An EINTR gets another try.
func openMkdirParent(parent string) (int, error) {
	for {
		fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}

// mkdirParentFailure gives the code of a failed parent open. A link is
// parent_elsewhere, ENOENT and ENOTDIR are parent_gone (c9125eb8, measured on
// Linux, ENOENT on macOS too). EACCES is denied (claustrum's own, not measured).
func mkdirParentFailure(parent string, err error) (rpc.MkdirResult, unix.Errno) {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		if fi, lerr := os.Lstat(parent); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			return rpc.MkdirResult{Code: "parent_elsewhere"}, 0
		}
	}
	switch {
	case errors.Is(err, unix.ENOENT):
		return rpc.MkdirResult{Code: "parent_gone", Errno: int(unix.ENOENT)}, 0
	case errors.Is(err, unix.ENOTDIR):
		return rpc.MkdirResult{Code: "parent_gone", Errno: int(unix.ENOTDIR)}, 0
	case errors.Is(err, unix.EACCES):
		return rpc.MkdirResult{Code: "denied", Errno: int(unix.EACCES)}, 0
	}
	return rpc.MkdirResult{}, mkdirErrno(err)
}

// mkdirExistsCode names what holds the name, with no link followed: a link is
// exists_link, a folder exists_folder and any other kind exists_other (c9125eb8,
// measured on Linux and macOS). A name that went before the stat is
// exists_other (claustrum's own, not measured).
func mkdirExistsCode(fd int, name string) string {
	var st unix.Stat_t
	if unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return "exists_other"
	}
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
