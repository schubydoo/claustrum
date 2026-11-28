package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The seams of the Linux place check. A test points them at its own files,
// because it can make no file of root and no FUSE mount.
var (
	// placeExePath names the program that runs.
	placeExePath = "/proc/self/exe"
	// placeOwnerUID is the owner that each folder and the file must have.
	placeOwnerUID uint32
	// placeWalkTop is the first folder of the walk. A test starts below the
	// shared temp folder, whose mode 1777 refuses each place under it.
	placeWalkTop = "/"
	// placeLoginUID is the account that signs in.
	placeLoginUID = uint32(os.Getuid())
	// placeFileSystem names the file system of an entry as statfs gives it:
	// "fuse", "nfs" or "" for any other. virtiofs gives the FUSE type too.
	placeFileSystem = func(path string) (string, error) {
		var st unix.Statfs_t
		if err := unix.Statfs(path, &st); err != nil {
			return "", err
		}
		return fileSystemName(int64(st.Type)), nil //nolint:unconvert // Type is int32 on some systems
	}
	// placeHasCaps answers whether a file carries file capabilities.
	placeHasCaps = func(path string) (bool, error) {
		_, err := unix.Getxattr(path, "security.capability", nil)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, unix.ENODATA), errors.Is(err, unix.ENOTSUP):
			return false, nil
		}
		return false, err
	}
)

// examinePlace is the Linux place check, in claustrum's order. A path that is not absolute is not_absolute. Then each
// folder from the top and the file itself must exist, be on no FUSE and no NFS,
// be owned by root and be writable by no group or other account. The first that
// fails names the refusal. Then the program that runs must be that file, the file
// must be regular with no setuid, setgid or capabilities, and the path must
// end in srv/<version>/server with no link among those three parts.
//
// Measured orders: a writable folder above comes before the shape, and
// another_file comes before the shape. On NFS the NFS text comes before an
// owner nobody, mode 0775 and another_file. Not measured: the order of the owner
// and the write test on one entry, of another_file and mode, a link above the three
// parts, and a non-regular file (claustrum: another_file). examineRunFrom bounds
// the whole check at 8 s.
func examinePlace(path, version string) *placeDenial {
	if !filepath.IsAbs(path) {
		return &placeDenial{kind: "not_absolute", path: path, text: notAbsoluteText}
	}
	var leaf os.FileInfo
	for _, p := range placeWalk(path) {
		fi, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return &placeDenial{kind: "missing", path: path, text: fmt.Sprintf("`%s`, on the way to the helper, does not exist", p)}
		}
		if err != nil {
			return placeUnreadable(path, p, err)
		}
		if r := testPlaceEntry(path, p, fi); r != nil {
			return r
		}
		leaf = fi
	}
	exe, err := os.Stat(placeExePath)
	if err != nil {
		return placeUnreadable(path, placeExePath, err)
	}
	if leaf == nil || !os.SameFile(exe, leaf) {
		return &placeDenial{kind: "another_file", path: path, text: anotherFileText}
	}
	caps, err := placeHasCaps(path)
	if err != nil {
		return placeUnreadable(path, path, err)
	}
	// A leaf that is not regular already answered another_file above, because
	// the program that runs is a regular file. The IsRegular test guards the
	// seam placeExePath only.
	if !leaf.Mode().IsRegular() || leaf.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || caps {
		return &placeDenial{kind: "mode", path: path, text: modeText}
	}
	return examineShape(path, version)
}

// placeWalk lists the folders from placeWalkTop down to path, and path last.
func placeWalk(path string) []string {
	var out []string
	for p := path; ; p = filepath.Dir(p) {
		if placeWalkTop == "/" || p == placeWalkTop || strings.HasPrefix(p, placeWalkTop+"/") {
			out = append(out, p)
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// fileSystemName names the statfs type of FUSE and NFS, and gives "" for
// any other type.
func fileSystemName(magic int64) string {
	switch magic {
	case unix.FUSE_SUPER_MAGIC:
		return "fuse"
	case unix.NFS_SUPER_MAGIC:
		return "nfs"
	}
	return ""
}

// nobodyUID is the owner that a login sees for a file whose real owner it
// cannot map. 13047a51 names uid 65534 so (measured on Linux).
const nobodyUID = 65534

// testPlaceEntry tests one entry of the walk: FUSE or NFS, then the owner, then
// the write access of a group or another account.
func testPlaceEntry(path, p string, fi os.FileInfo) *placeDenial {
	kind, err := placeFileSystem(p)
	if err != nil {
		return placeUnreadable(path, p, err)
	}
	switch kind {
	case "fuse":
		return &placeDenial{kind: "cannot_tell", path: path, text: fmt.Sprintf("`%s` is on FUSE or virtiofs, so who can change it can't be checked. Keep the helper and every folder above it on a local disk, such as ext4 or XFS, or in a container's image.", p)}
	case "nfs":
		return &placeDenial{kind: "cannot_tell", path: path, text: fmt.Sprintf("`%s` is on NFS, so who can change it can't be checked. Keep the helper and every folder above it on a local disk, such as ext4 or XFS, or in a container's image.", p)}
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return placeUnreadable(path, p, errors.New("no owner"))
	}
	if st.Uid != placeOwnerUID {
		switch st.Uid {
		case nobodyUID:
			return &placeDenial{kind: "cannot_tell", path: path, text: fmt.Sprintf("`%s` shows as uid %d (nobody), which usually means this login can't see its real owner, as in a sandbox that doesn't map root or on a squashed network export. This login must see root as the owner of the helper and every folder above it.", p, st.Uid)}
		case placeLoginUID:
			return &placeDenial{kind: "owner", path: path, text: fmt.Sprintf("`%s` belongs to the account that signs in (uid %d). Root must own the helper and every folder above it.", p, st.Uid)}
		}
		return &placeDenial{kind: "owner", path: path, text: fmt.Sprintf("`%s` belongs to uid %d. Root must own the helper and every folder above it.", p, st.Uid)}
	}
	if st.Mode&0o022 != 0 {
		return &placeDenial{kind: "writable", path: path, text: fmt.Sprintf("`%s` can be changed by accounts other than its owner (mode %04o). Remove that write access.", p, st.Mode&0o7777)}
	}
	return nil
}

// examineShape tests that the path ends in srv/<version>/server, and that
// none of those three parts is a link.
func examineShape(path, version string) *placeDenial {
	refused := &placeDenial{kind: "shape", path: path, text: "The path must end in `srv/" + version + "/server`, this helper's own version, with no link among those three parts."}
	p := path
	for _, want := range []string{"server", version, "srv"} {
		if filepath.Base(p) != want {
			return refused
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return placeUnreadable(path, p, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return refused
		}
		p = filepath.Dir(p)
	}
	return nil
}

// placeUnreadable is the refusal of an entry that a test failed to read. The
// text names the error of the call, with no call name and no path, as
// 13047a51 does for a stat of the walk (measured on Linux: permission denied,
// and transport endpoint is not connected on FUSE). For the program that
// runs, the capability read and the shape test the same text is claustrum's
// own, not measured.
func placeUnreadable(path, p string, err error) *placeDenial {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return &placeDenial{kind: "cannot_tell", path: path, text: fmt.Sprintf("`%s`, on the way to the helper, cannot be checked: %v", p, err)}
}
