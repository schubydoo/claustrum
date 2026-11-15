//go:build !windows

package git

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// storeInCheckout says whether the read-tree call of a create in the state
// shadow names the LFS store of its `.checkout-<n>` folder. 4c111e86 does so
// on Linux and macOS (measured).
const storeInCheckout = true

// linkIntoRoot makes name in root a hard link to the file src.
func linkIntoRoot(root *os.Root, src, name string) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return unix.Linkat(unix.AT_FDCWD, src, int(d.Fd()), name, 0)
}

// hardLinks is the link count of the file of fi, or 1 when fi has none.
func hardLinks(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink) //nolint:unconvert // Nlink is uint16 on macOS
	}
	return 1
}

// createSeedFile makes the new file name of dir for a copy of the seed or of
// the keep, with O_EXCL and O_NOFOLLOW, write only, mode perm.
func createSeedFile(dir *os.Root, name string, perm os.FileMode) (*os.File, error) {
	return dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
}

// linkAcrossRoots makes name of to a hard link to the file name of from, by one
// linkat that follows no link.
func linkAcrossRoots(from *os.Root, name string, to *os.Root, newName string) error {
	a, err := from.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	b, err := to.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	return unix.Linkat(int(a.Fd()), name, int(b.Fd()), newName, 0)
}

// openObjectNoFollow opens the regular file at path for reading. A link at
// its last name fails. O_NONBLOCK keeps a FIFO at path from blocking the open,
// and the FIFO then fails the test of a regular file.
func openObjectNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, os.ErrInvalid
	}
	return f, nil
}

// removeFileAt removes the file name of root by one unlinkat with no
// AT_REMOVEDIR. A folder at name stays.
func removeFileAt(root *os.Root, name string) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return unix.Unlinkat(int(d.Fd()), name, 0)
}

// freeBytes is the space of the file system of path that a user can write,
// and false when statfs fails.
func freeBytes(path string) (uint64, bool) {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}
