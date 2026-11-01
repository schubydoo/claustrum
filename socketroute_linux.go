package main

import (
	"io"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// maxSocketPath is the longest socket path that the socket address of Linux holds.
// `be1c1ca5` names 107 bytes in its text (measured on Linux).
const maxSocketPath = 107

// procSelfFD is the folder of the open handles of this process. A var so a test can
// take the /proc route away.
var procSelfFD = "/proc/self/fd"

// folderHandleName opens a handle of dir and runs use on the name
// /proc/self/fd/<n>/<base>. ok is false, and use does not run, when that name does
// not fit or does not lead to the folder of the handle. The identity test of the
// handle and the name is claustrum's own: `be1c1ca5` shows the route, not its test
// (not measured).
func folderHandleName[T io.Closer](dir, base string, use func(name string) (T, error)) (v T, ok bool, err error) {
	fd, oerr := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if oerr != nil {
		return v, false, nil
	}
	f := os.NewFile(uintptr(fd), dir)
	defer f.Close()
	link := procSelfFD + "/" + strconv.Itoa(fd)
	name := link + "/" + base
	if len(name) > maxSocketPath {
		return v, false, nil
	}
	held, herr := f.Stat()
	seen, serr := os.Stat(link)
	if herr != nil || serr != nil || !os.SameFile(held, seen) {
		return v, false, nil
	}
	v, err = use(name)
	return v, true, err
}

// openWorkingFolder opens a handle of the working folder that a change of folder
// can return to. O_PATH needs no read permission on the folder.
func openWorkingFolder() (*os.File, error) {
	fd, err := unix.Open(".", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: ".", Err: err}
	}
	return os.NewFile(uintptr(fd), "."), nil
}
