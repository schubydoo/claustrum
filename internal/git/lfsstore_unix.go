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
