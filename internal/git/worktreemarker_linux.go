package git

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// markerCloseStops says whether a failed close of the new marker stops the
// create. c9125eb8 and 4c111e86 stop it on Linux (measured).
const markerCloseStops = true

// openMarkerFile makes the new marker in dir with O_EXCL and opens it for
// writing. It opens dir as a path handle and makes the marker by openat in it.
// The file is named /dev/fd/<handle>/.claude-managed-worktrees, so a failed
// close reads "close /dev/fd/<n>/.claude-managed-worktrees: <errno>".
// c9125eb8 and 4c111e86 name the file so on Linux (measured, with n 14). The
// number here is that of claustrum's handle. release closes the handle, after
// the close of the file. An error is the bare errno.
func openMarkerFile(dir string) (f *os.File, release func(), err error) {
	dfd, err := retryEINTR(func() (int, error) {
		return unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return nil, nil, err
	}
	release = func() { _ = unix.Close(dfd) }
	fd, err := retryEINTR(func() (int, error) {
		return unix.Openat(dfd, managedWorktreesMarker, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0o644)
	})
	if err != nil {
		release()
		return nil, nil, err
	}
	return os.NewFile(uintptr(fd), fmt.Sprintf("/dev/fd/%d/%s", dfd, managedWorktreesMarker)), release, nil
}

// retryEINTR runs open again while it fails with EINTR, as package os does.
func retryEINTR(open func() (int, error)) (int, error) {
	for {
		fd, err := open()
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}
