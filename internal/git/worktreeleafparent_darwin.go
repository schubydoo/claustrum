package git

import (
	"errors"

	"golang.org/x/sys/unix"
)

// openLeafParent opens dir with O_SEARCH for removeWorktreeLeaf. O_SEARCH needs search
// permission on dir and no read permission, so a parent with write and search
// permission alone still gives the handle (claustrum's own, not measured). An error is
// the bare errno.
func openLeafParent(dir string) (int, error) {
	for {
		fd, err := unix.Open(dir, unix.O_SEARCH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}

// unlinkFoundFolder reports whether an unlinkat with no flag failed on a directory.
// macOS answers EPERM there, and EISDIR counts too (claustrum's own, not measured).
func unlinkFoundFolder(err error) bool {
	return errors.Is(err, unix.EPERM) || errors.Is(err, unix.EISDIR)
}
