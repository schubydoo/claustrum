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
// a chain of 41 links, a loop of 2 and a self loop get links, with a right or a wrong
// digest. Chains of 5, 39 and 40 pass. Linux only, because the link limit of the
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
}

// TestStatLauncherFactsFileSystem pins the f_type test through the statfs seam: NFS,
// FUSE, CIFS and SMB2 get their names, ext4 and tmpfs get none. A failed statfs is an
// error.
func TestStatLauncherFactsFileSystem(t *testing.T) {
	d := launcherDir(t)
	old := launcherFSType
	t.Cleanup(func() { launcherFSType = old })
	for _, c := range []struct {
		magic uint32
		want  string
	}{
		{unix.NFS_SUPER_MAGIC, "NFS"},
		{unix.FUSE_SUPER_MAGIC, "FUSE or virtiofs"},
		{unix.CIFS_SUPER_MAGIC, "SMB"},
		{unix.SMB2_SUPER_MAGIC, "SMB"},
		{unix.EXT4_SUPER_MAGIC, ""},
		{unix.TMPFS_MAGIC, ""},
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
