//go:build linux

package install

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
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

// launcherNinePMounted reports whether the mount table of the daemon lists a 9p mount.
// A seam, so a test fakes the table. Production never reassigns it.
var launcherNinePMounted = ninePInMountTable

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
	f := launcherFacts{uid: st.Uid, mode: st.Mode & 0o7777, fs: launcherFSName(magic)}
	// A disk kind with no real device, while a 9p mount is listed, gets the 9p text
	// (4c111e86, measured on Linux: btrfs, and the ext4 f_type on a tmpfs, both with a
	// 9p mount). With no 9p mount the item passes as before (not measured).
	if f.fs == "" && launcherDiskKind(magic) && unix.Major(st.Dev) == 0 && launcherNinePMounted() {
		f.like9p = true
	}
	return f, nil
}

// launcherFSName names the file system of an f_type as the refusal text shows it, or
// "" for a file system that the walk trusts. The set that the walk trusts is closed:
// any other f_type gets the unknown text with its hex (4c111e86, measured on Linux,
// with vfat, udf, iso9660 and 0x12345678). The names follow 4c111e86 (measured on
// Linux): 9p, NFS and SMB on real mounts, the others by a rewritten f_type. FUSE keeps
// its text of the earlier rows.
func launcherFSName(magic uint32) string {
	switch magic {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.EROFS_SUPER_MAGIC_V1, unix.BTRFS_SUPER_MAGIC:
		// ext4, xfs and erofs passed on Linux (4c111e86). btrfs got the 9p text, not the
		// unknown text, so it is in the set too.
		return ""
	case unix.TMPFS_MAGIC, unix.OVERLAYFS_SUPER_MAGIC, zfsMagic, unix.SQUASHFS_MAGIC, unix.F2FS_SUPER_MAGIC, unix.RAMFS_MAGIC:
		// Trusted until a row decides (claustrum's own, not measured: the tmpfs rows
		// rewrote the f_type, and no row ran on the others). They are no disk kinds of the
		// 9p rule. kAFS (0x6b414653) has no row and gets the unknown text.
		return ""
	case unix.ECRYPTFS_SUPER_MAGIC, jfsMagic, unix.REISERFS_SUPER_MAGIC, unix.BCACHEFS_SUPER_MAGIC:
		// Local kinds with Unix ownership, trusted (claustrum's own, not measured). They
		// are no disk kinds of the 9p rule. exFAT (0x2011bab0) and NTFS3 (0x7366746e)
		// keep no Unix owner and get the unknown text, like the measured vfat (claustrum's
		// own, not measured).
		return ""
	case unix.NFS_SUPER_MAGIC:
		return "NFS"
	case unix.FUSE_SUPER_MAGIC:
		return "FUSE or virtiofs"
	case unix.CIFS_SUPER_MAGIC, unix.SMB2_SUPER_MAGIC:
		return "SMB"
	case unix.V9FS_MAGIC:
		return "9p"
	case unix.AFS_SUPER_MAGIC:
		return "AFS"
	case unix.CEPH_SUPER_MAGIC:
		return "Ceph"
	case gfs2Magic:
		return "GFS2"
	case lustreMagic:
		return "Lustre"
	case gpfsMagic:
		return "GPFS"
	case unix.OCFS2_SUPER_MAGIC:
		return "OCFS2"
	case vxfsMagic:
		return "VxFS"
	}
	return fmt.Sprintf("an unknown kind of file system (%#x)", magic)
}

// The f_type values that x/sys/unix does not name. All but zfsMagic and jfsMagic are as
// the rows wrote them. jfsMagic is the value of the kernel.
const (
	jfsMagic    = 0x3153464a
	zfsMagic    = 0x2fc12fc1
	gfs2Magic   = 0x01161970
	lustreMagic = 0x0bd00bd0
	gpfsMagic   = 0x47504653
	vxfsMagic   = 0xa501fcf5
)

// launcherDiskKind reports a trusted kind that lives on a disk. xfs and erofs are in it
// by the same rule (claustrum's own, not measured: their rows had a real device).
func launcherDiskKind(magic uint32) bool {
	switch magic {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.EROFS_SUPER_MAGIC_V1, unix.BTRFS_SUPER_MAGIC:
		return true
	}
	return false
}

// ninePInMountTable reads /proc/self/mountinfo for a mount of type 9p. A table that
// cannot be read lists none. The source of the test is claustrum's own, not measured:
// the rows held the 9p mount in the namespace of the daemon.
func ninePInMountTable() bool {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	defer f.Close()
	return ninePListed(f)
}

// ninePListed reports whether a mountinfo table lists a mount of type 9p.
func ninePListed(r io.Reader) bool {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if mountInfoType(sc.Text()) == "9p" {
			return true
		}
	}
	return false
}

// mountInfoType returns the file system type of one mountinfo line: the first field
// after the " - " separator.
func mountInfoType(line string) string {
	_, rest, ok := strings.Cut(line, " - ")
	if !ok {
		return ""
	}
	typ, _, _ := strings.Cut(rest, " ")
	return typ
}
