package main

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// maxSocketPath is the longest socket path that the socket address of macOS holds.
// `be1c1ca5` names 103 bytes in its text, and applies the limit to the last element
// of the path (measured on macOS).
const maxSocketPath = 103

// folderHandleName never applies on macOS: it has no /proc route.
func folderHandleName[T io.Closer](string, string, func(string) (T, error)) (v T, ok bool, err error) {
	return v, false, nil
}

// openWorkingFolder opens a handle of the working folder that a change of folder
// can return to. O_SEARCH needs no read permission on the folder.
func openWorkingFolder() (*os.File, error) {
	fd, err := unix.Open(".", unix.O_SEARCH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: ".", Err: err}
	}
	return os.NewFile(uintptr(fd), "."), nil
}
