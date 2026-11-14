package git

import (
	"errors"

	"golang.org/x/sys/unix"
)

// openLeafParent opens dir as a path handle for removeWorktreeLeaf. O_PATH needs no
// read permission on dir, so a parent with write and search permission alone still
// gives the handle. An error is the bare errno.
func openLeafParent(dir string) (int, error) {
	return retryEINTR(func() (int, error) {
		return unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
}

// unlinkFoundFolder reports whether an unlinkat with no flag failed on a directory.
// Linux answers EISDIR (measured with c9125eb8 and 4c111e86).
func unlinkFoundFolder(err error) bool {
	return errors.Is(err, unix.EISDIR)
}
