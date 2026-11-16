//go:build linux

package install

import (
	"fmt"
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

// launcherBDINames reads the names in /sys/class/bdi. A seam, so a test fakes the
// folder. Production never reassigns it.
var launcherBDINames = readBDINames

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
	f := launcherFacts{uid: st.Uid, mode: st.Mode & 0o7777, fs: launcherFSName(magic), magic: magic}
	if f.fs == "" && launcherNinePKind(magic) && unix.Major(st.Dev) == 0 {
		f.doubt = launcherNinePDoubt(magic)
	}
	return f, nil
}

// launcherNinePDoubt gives the doubt of a kind of the 9p rule with no real device
// (4c111e86, measured on Linux). A 9p entry in /sys/class/bdi gives doubtNineP, also for
// a 9p mount outside the mount namespace of the daemon. A folder that can't be read or
// is empty gives doubtNoBDI (measured with btrfs, tmpfs, ramfs and overlay). With no 9p
// entry, the f_type of ext4 or XFS gives doubtNoDisk (measured on tmpfs and ramfs). The
// order of the three tests is claustrum's own where no row shows two of them.
func launcherNinePDoubt(magic uint32) int {
	names, err := launcherBDINames()
	if err != nil || len(names) == 0 {
		return doubtNoBDI
	}
	for _, n := range names {
		if isNinePBDI(n) {
			return doubtNineP
		}
	}
	if magic == unix.EXT4_SUPER_MAGIC || magic == unix.XFS_SUPER_MAGIC {
		return doubtNoDisk
	}
	return doubtNone
}

// isNinePBDI reports a bdi name of a 9p mount: "9p-" and a number.
func isNinePBDI(name string) bool {
	n, ok := strings.CutPrefix(name, "9p-")
	if !ok || n == "" {
		return false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// readBDINames lists /sys/class/bdi.
func readBDINames() ([]string, error) {
	ents, err := os.ReadDir("/sys/class/bdi")
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out, err
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
		// Trusted (claustrum's own, not measured for ZFS, squashfs and f2fs). tmpfs, ramfs
		// and overlay are kinds of the 9p rule (4c111e86, measured on Linux). kAFS
		// (0x6b414653) has no row and gets the unknown text.
		return ""
	case unix.ECRYPTFS_SUPER_MAGIC, jfsMagic, unix.REISERFS_SUPER_MAGIC, unix.BCACHEFS_SUPER_MAGIC:
		// Local kinds with Unix ownership, trusted (claustrum's own, not measured). They
		// are no kinds of the 9p rule. exFAT (0x2011bab0) and NTFS3 (0x7366746e)
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

// launcherNinePKind reports a trusted kind of the 9p rule. btrfs, tmpfs, ramfs and
// overlay are measured on Linux (4c111e86), and the f_type of ext4 or XFS on a tmpfs.
// erofs is in it by the same rule (claustrum's own, not measured). The other trusted
// kinds are not (claustrum's own, not measured).
func launcherNinePKind(magic uint32) bool {
	switch magic {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.EROFS_SUPER_MAGIC_V1, unix.BTRFS_SUPER_MAGIC,
		unix.TMPFS_MAGIC, unix.RAMFS_MAGIC, unix.OVERLAYFS_SUPER_MAGIC:
		return true
	}
	return false
}
