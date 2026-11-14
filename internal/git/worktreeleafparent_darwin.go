package git

import (
	"errors"

	"golang.org/x/sys/unix"
)

// openLeafParent opens dir with O_SEARCH for rmdirWorktreeLeaf. O_SEARCH needs search
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
