//go:build linux

package install

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// The check by handle (launcher.checked_by_handle). The whole step is claustrum's own,
// not measured: no row of 4c111e86 shows a reply for a path that moved during the check.
// Before the walk, the check looks at "/", each folder down to the launcher after its
// links, and the launcher, by path. After the walk and the digest it opens the same
// items again, each one from the handle of the folder above it, with no link followed.
// An item whose device, inode or mount id differs moved, and the launcher is refused.

// launcherID is the device, the inode and the mount id of one item.
type launcherID struct {
	dev, ino, mnt uint64
}

const launcherIDMask = unix.STATX_INO | unix.STATX_MNT_ID

func launcherIDOf(st *unix.Statx_t) launcherID {
	return launcherID{dev: unix.Mkdev(st.Dev_major, st.Dev_minor), ino: st.Ino, mnt: st.Mnt_id}
}

// launcherParts is "/" and each name of the absolute, link-free path target.
func launcherParts(target string) []string {
	return append([]string{string(filepath.Separator)}, strings.Split(strings.TrimPrefix(filepath.Clean(target), "/"), "/")...)
}

// lookLauncherPath reads the launcherID of each item of target by path, with no link
// followed at the last name.
func lookLauncherPath(target string) ([]launcherID, error) {
	var out []launcherID
	for _, item := range launcherFolders(filepath.Dir(target)) {
		id, err := statxPath(item)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	id, err := statxPath(target)
	if err != nil {
		return nil, err
	}
	return append(out, id), nil
}

func statxPath(p string) (launcherID, error) {
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, p, unix.AT_SYMLINK_NOFOLLOW, launcherIDMask, &st); err != nil {
		return launcherID{}, err
	}
	return launcherIDOf(&st), nil
}

// launcherMoved walks target by handle and compares each item with look. The first
// item that differs, or that does not open, is refused with fault writable. The text is
// claustrum's own, not measured.
func launcherMoved(target string, look []launcherID) (fault, reason string) {
	parts := launcherParts(target)
	fd := -1
	defer func() {
		if fd >= 0 {
			unix.Close(fd)
		}
	}()
	for i, name := range parts {
		item := filepath.Join(parts[:i+1]...)
		var next int
		var err error
		if i == 0 {
			next, err = unix.Open(name, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		} else {
			next, err = unix.Openat(fd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if err == nil {
			if fd >= 0 {
				unix.Close(fd)
			}
			fd = next
			var st unix.Statx_t
			err = unix.Statx(fd, "", unix.AT_EMPTY_PATH, launcherIDMask, &st)
			if err == nil && (i >= len(look) || launcherIDOf(&st) != look[i]) {
				err = unix.ESTALE
			}
		}
		if err != nil {
			return FaultWritable, "`" + managedShown(item) + "` changed while the launcher was checked, so the launcher can't be trusted. Keep the launcher somewhere that no other account can change."
		}
	}
	return "", ""
}
