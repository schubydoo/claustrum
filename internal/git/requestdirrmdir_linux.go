package git

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// rmdirInFolder removes the empty folder name in folder by one unlinkat with
// AT_REMOVEDIR through a handle of folder. The handle is opened with O_PATH,
// which needs only the search bit, and follows no link at folder. It must have
// the identity parent, so a folder or link that replaced folder or a folder
// above it gets errParentSwapped (claustrum's own).
func rmdirInFolder(folder, name string, parent os.FileInfo) error {
	var fd int
	var err error
	for {
		fd, err = unix.Open(folder, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if !handleIs(fd, parent) {
		return errParentSwapped
	}
	return unix.Unlinkat(fd, name, unix.AT_REMOVEDIR)
}
