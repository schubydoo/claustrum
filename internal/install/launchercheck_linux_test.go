//go:build linux

package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// linkChain makes n links in d, each to the one before, the first to target. It
// returns the last link.
func linkChain(t *testing.T, d, name, target string, n int) string {
	t.Helper()
	prev := target
	for i := range n {
		next := filepath.Join(d, fmt.Sprintf("%s%02d", name, i+1))
		if err := os.Symlink(prev, next); err != nil {
			t.Fatal(err)
		}
		prev = next
	}
	return prev
}

// TestCheckLauncherFileLinkLoop pins the links fault of c9125eb8 (measured on Linux):
// a chain of 41 links, a loop of 2 and a self loop get links with a right digest, the
// loop of 2 with a wrong one too. Chains of 5, 39 and 40 pass. Linux only, because the link limit of the
// system differs elsewhere.
func TestCheckLauncherFileLinkLoop(t *testing.T) {
	d := launcherDir(t)
	ok := writeLauncher(t, filepath.Join(d, "ok", "launcher.sh"), "#!/bin/sh\n", 0o755)
	fakeLauncherFacts(t, nil)
	right := LauncherCheck{SHA256: []string{digestOf(t, ok)}}
	for _, n := range []int{5, 39, 40} {
		L := linkChain(t, d, fmt.Sprintf("pass%d-", n), ok, n)
		checkFile(t, fmt.Sprintf("chain of %d", n), L, right, "", "")
	}
	links := func(L string) string {
		return "too many symbolic links (or a loop) lead to launcher `" + L + "`"
	}
	chain41 := linkChain(t, d, "chain41-", ok, 41)
	checkFile(t, "chain of 41", chain41, right, FaultLinks, links(chain41))
	loopa, loopb := filepath.Join(d, "loopa"), filepath.Join(d, "loopb")
	if err := os.Symlink(loopb, loopa); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(loopa, loopb); err != nil {
		t.Fatal(err)
	}
	checkFile(t, "loop of 2", loopa, right, FaultLinks, links(loopa))
	checkFile(t, "loop of 2, wrong digest", loopa, LauncherCheck{SHA256: []string{zeroDigest}}, FaultLinks, links(loopa))
	self := filepath.Join(d, "loopself")
	if err := os.Symlink(self, self); err != nil {
		t.Fatal(err)
	}
	checkFile(t, "self loop", self, right, FaultLinks, links(self))
	loopdir := filepath.Join(d, "loopdir")
	if err := os.Symlink(loopdir, loopdir); err != nil {
		t.Fatal(err)
	}
	mid := filepath.Join(loopdir, "x")
	checkFile(t, "loop in a folder of the path", mid, right, FaultLinks, links(mid))
}

// TestCheckLauncherFileMoved pins that a moved path is not refused for the move
// (4c111e86, measured on Linux). The facts seam swaps the folder x for a new one while
// the walk tests x. A swap to the same launcher passes, with no digest and with one. A
// swap to another file gets the digest refusal.
func TestCheckLauncherFileMoved(t *testing.T) {
	d := launcherDir(t)
	x := filepath.Join(d, "x")
	L := writeLauncher(t, filepath.Join(x, "launcher.sh"), "#!/bin/sh\n", 0o755)
	fakeLauncherFacts(t, nil)
	right := LauncherCheck{SHA256: []string{digestOf(t, L)}}
	none := "launcher `" + L + "` matches none of the SHA-256 digests in the policy. Run sha256sum on the host to see its digest."
	for i, c := range []struct {
		chk         LauncherCheck
		body        string
		fault, text string
	}{
		{LauncherCheck{}, "#!/bin/sh\n", "", ""},
		{right, "#!/bin/sh\n", "", ""},
		{right, "#!/bin/sh\nexit 0\n", FaultDigest, none},
	} {
		swapped := false
		launcherPathFacts = func(p string) (launcherFacts, error) {
			if p == x && !swapped {
				swapped = true
				if err := os.Rename(x, filepath.Join(d, fmt.Sprintf("old%d", i))); err != nil {
					return launcherFacts{}, err
				}
				writeLauncher(t, L, c.body, 0o755)
			}
			return launcherFacts{mode: 0o755}, nil
		}
		checkFile(t, fmt.Sprintf("swap %d", i), L, c.chk, c.fault, c.text)
		if !swapped {
			t.Fatalf("swap %d: the walk never tested %s", i, x)
		}
	}
}

// TestLauncherFSTypeReal pins that the f_type seam reads the real statfs: /proc gives
// the proc f_type, which is no trusted kind.
func TestLauncherFSTypeReal(t *testing.T) {
	magic, err := launcherFSType("/proc")
	if err != nil || magic != unix.PROC_SUPER_MAGIC {
		t.Fatalf("f_type of /proc: got %#x, %v, want %#x", magic, err, unix.PROC_SUPER_MAGIC)
	}
	f, err := statLauncherFacts("/proc")
	if want := "an unknown kind of file system (0x9fa0)"; err != nil || f.fs != want {
		t.Errorf("facts of /proc: got %q, %v, want %q", f.fs, err, want)
	}
}

// TestStatLauncherFactsLike9p pins the 9p rule through the seams (4c111e86, measured on
// Linux). /proc is on a device with major 0. The folder of the test gives a real device
// where it has one. bdi is the list of /sys/class/bdi, nil for a folder that can't be read.
func TestStatLauncherFactsLike9p(t *testing.T) {
	old, oldBDI := launcherFSType, launcherBDINames
	t.Cleanup(func() { launcherFSType, launcherBDINames = old, oldBDI })
	nine := []string{"0:62", "259:0", "9p-1"}
	plain := []string{"0:62", "259:0", "btrfs-1"}
	type row struct {
		p     string
		magic uint32
		bdi   []string
		want  int
	}
	rows := []row{
		{"/proc", unix.EXT4_SUPER_MAGIC, nine, doubtNineP},
		{"/proc", unix.BTRFS_SUPER_MAGIC, nine, doubtNineP},
		{"/proc", unix.XFS_SUPER_MAGIC, nine, doubtNineP},
		{"/proc", unix.TMPFS_MAGIC, nine, doubtNineP},
		{"/proc", unix.RAMFS_MAGIC, nine, doubtNineP},
		{"/proc", unix.OVERLAYFS_SUPER_MAGIC, nine, doubtNineP},
		{"/proc", unix.TMPFS_MAGIC, []string{"9p-12"}, doubtNineP},
		{"/proc", unix.TMPFS_MAGIC, []string{"9p-", "9p-x", "x9p-1"}, doubtNone},
		{"/proc", unix.BTRFS_SUPER_MAGIC, nil, doubtNoBDI},
		{"/proc", unix.TMPFS_MAGIC, nil, doubtNoBDI},
		{"/proc", unix.RAMFS_MAGIC, []string{}, doubtNoBDI},
		{"/proc", unix.OVERLAYFS_SUPER_MAGIC, nil, doubtNoBDI},
		{"/proc", unix.EXT4_SUPER_MAGIC, plain, doubtNoDisk},
		{"/proc", unix.XFS_SUPER_MAGIC, plain, doubtNoDisk},
		{"/proc", unix.BTRFS_SUPER_MAGIC, plain, doubtNone},
		{"/proc", unix.EROFS_SUPER_MAGIC_V1, plain, doubtNone},
		{"/proc", unix.TMPFS_MAGIC, plain, doubtNone},
		{"/proc", 0x2fc12fc1, nine, doubtNone},
		{"/proc", unix.SQUASHFS_MAGIC, nil, doubtNone},
		{"/proc", unix.NFS_SUPER_MAGIC, nine, doubtNone},
	}
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(d, &st); err != nil {
		t.Fatal(err)
	}
	if unix.Major(st.Dev) != 0 {
		rows = append(rows, row{d, unix.EXT4_SUPER_MAGIC, nine, doubtNone}, row{d, unix.EXT4_SUPER_MAGIC, nil, doubtNone})
	} else {
		t.Logf("%s is on a device with major 0: the two rows of a real device are skipped", d)
	}
	for _, c := range rows {
		launcherFSType = func(string) (uint32, error) { return c.magic, nil }
		launcherBDINames = func() ([]string, error) {
			if c.bdi == nil {
				return nil, os.ErrPermission
			}
			return c.bdi, nil
		}
		f, err := statLauncherFacts(c.p)
		if err != nil || f.doubt != c.want || f.magic != c.magic {
			t.Errorf("%s, f_type %#x, bdi %q: got %d %#x, %v, want %d", c.p, c.magic, c.bdi, f.doubt, f.magic, err, c.want)
		}
	}
}

// TestReadBDINames pins the read of the real /sys/class/bdi of the test. An entry can
// come or go between two reads, so each name of the call must be in a read right after
// it, in any order, and most of them must be there.
func TestReadBDINames(t *testing.T) {
	got, err := readBDINames()
	ents, err2 := os.ReadDir("/sys/class/bdi")
	if err2 != nil || len(ents) == 0 {
		t.Skip("no /sys/class/bdi entries to compare:", err2)
	}
	if err != nil {
		t.Fatalf("readBDINames: %v", err)
	}
	after := map[string]bool{}
	for _, e := range ents {
		after[e.Name()] = true
	}
	missing := 0
	for _, n := range got {
		if !after[n] {
			missing++
		}
	}
	if len(got) == 0 || missing > 1 {
		t.Errorf("got %q, and %d of them are not in the read after it %v", got, missing, after)
	}
}

// TestStatLauncherFactsFileSystem pins the f_type test through the statfs seam: each
// kind that 4c111e86 names gets its name (measured on Linux), the trusted kinds get none,
// and any other f_type gets the unknown text with its hex. A failed statfs is an error.
func TestStatLauncherFactsFileSystem(t *testing.T) {
	d := launcherDir(t)
	old, oldBDI := launcherFSType, launcherBDINames
	t.Cleanup(func() { launcherFSType, launcherBDINames = old, oldBDI })
	launcherBDINames = func() ([]string, error) { return []string{"0:62"}, nil }
	for _, c := range []struct {
		magic uint32
		want  string
	}{
		{unix.NFS_SUPER_MAGIC, "NFS"},
		{unix.FUSE_SUPER_MAGIC, "FUSE or virtiofs"},
		{unix.CIFS_SUPER_MAGIC, "SMB"},
		{unix.SMB2_SUPER_MAGIC, "SMB"},
		{unix.V9FS_MAGIC, "9p"},
		{0x5346414f, "AFS"},
		{0x00c36400, "Ceph"},
		{0x01161970, "GFS2"},
		{0x0bd00bd0, "Lustre"},
		{0x47504653, "GPFS"},
		{0x7461636f, "OCFS2"},
		{0xa501fcf5, "VxFS"},
		{0x4d44, "an unknown kind of file system (0x4d44)"},
		{0x15013346, "an unknown kind of file system (0x15013346)"},
		{0x9660, "an unknown kind of file system (0x9660)"},
		{0x12345678, "an unknown kind of file system (0x12345678)"},
		{unix.EXT4_SUPER_MAGIC, ""},
		{unix.XFS_SUPER_MAGIC, ""},
		{unix.EROFS_SUPER_MAGIC_V1, ""},
		{unix.BTRFS_SUPER_MAGIC, ""},
		{unix.TMPFS_MAGIC, ""},
		{unix.OVERLAYFS_SUPER_MAGIC, ""},
		{0x2fc12fc1, ""},
		{unix.SQUASHFS_MAGIC, ""},
		{unix.F2FS_SUPER_MAGIC, ""},
		{unix.RAMFS_MAGIC, ""},
		{0xf15f, ""},
		{0x3153464a, ""},
		{0x52654973, ""},
		{0xca451a4e, ""},
		{0x2011bab0, "an unknown kind of file system (0x2011bab0)"},
		{0x7366746e, "an unknown kind of file system (0x7366746e)"},
		{0x6b414653, "an unknown kind of file system (0x6b414653)"},
	} {
		launcherFSType = func(p string) (uint32, error) {
			if p != d {
				return 0, fmt.Errorf("statfs of %q, want %q", p, d)
			}
			return c.magic, nil
		}
		f, err := statLauncherFacts(d)
		if err != nil || f.fs != c.want {
			t.Errorf("f_type %#x: got %q, %v, want %q", c.magic, f.fs, err, c.want)
		}
	}
	launcherFSType = func(string) (uint32, error) { return 0, errors.New("statfs failed") }
	if _, err := statLauncherFacts(d); err == nil {
		t.Error("a failed statfs gave no error")
	}
}
