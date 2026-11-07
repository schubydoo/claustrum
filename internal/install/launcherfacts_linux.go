//go:build linux

package install

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// launcherFSType reads the f_type of statfs on a path. A seam, so a test gives a fake
// f_type. Production never reassigns it.
var launcherFSType = func(p string) (uint32, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(p, &fs); err != nil {
		return 0, err
	}
	return uint32(fs.Type), nil
}

// statLauncherFacts reads the owner, the mode bits and the file system type of p. It
// follows a link, so a folder of the path as written that is a link gives the facts
// of its target. The walk tests the holder folder of each link too.
func statLauncherFacts(p string) (launcherFacts, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return launcherFacts{}, err
	}
	magic, err := launcherFSType(p)
	if err != nil {
		return launcherFacts{}, err
	}
	return launcherFacts{uid: st.Uid, mode: st.Mode & 0o7777, fs: launcherFSName(magic)}, nil
}

// launcherFSName names the file system of an f_type as the refusal text shows it, or
// "" for a file system that the walk trusts. The f_type test is claustrum's own, not
// measured: the rows staged NFS, FUSE (bindfs) and CIFS (vers=3.0) on Linux. virtiofs
// reports the FUSE f_type and was not staged. The two SMB values cover a mount with
// SMB1 and one with a later dialect. 9p was not staged, so it is not named.
func launcherFSName(magic uint32) string {
	switch magic {
	case unix.NFS_SUPER_MAGIC:
		return "NFS"
	case unix.FUSE_SUPER_MAGIC:
		return "FUSE or virtiofs"
	case unix.CIFS_SUPER_MAGIC, unix.SMB2_SUPER_MAGIC:
		return "SMB"
	}
	return ""
}
