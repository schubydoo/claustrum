//go:build unix

package git

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// rmdirHeadCopy removes the entry HEAD of root, the folder dir of a
// repository copy, only if it is an empty folder: one unlinkat with
// AT_REMOVEDIR through a handle of dir. The handle must be the folder of root.
// A file or a link at HEAD fails with ENOTDIR and stays, and so does a folder
// with content (claustrum's own).
func rmdirHeadCopy(root *os.Root, dir string) error {
	want, err := root.Stat(".")
	if err != nil {
		return err
	}
	var fd int
	for {
		fd, err = unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if !handleIs(fd, want) {
		return errParentSwapped
	}
	return unix.Unlinkat(fd, "HEAD", unix.AT_REMOVEDIR)
}
