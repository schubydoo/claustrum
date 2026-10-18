package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// renameHeld reports whether a rename failed because another process holds
// the entry: access denied or a sharing violation. On 5fd08069 a copy that is
// open without delete sharing, and a deny entry on `.git\worktrees`, give
// "Access is denied." (round task009-01, Windows VM). The sharing violation is
// claustrum's own choice (not measured).
func renameHeld(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// copyRenameError gives a failed rename of the copy write the form of
// os.Rename, with the full path of both names below root. On 5fd08069 the
// text is `rename <temp file> <copy>: Access is denied.` with both full paths
// (cell F1b of round task009-01, Windows VM). The rename itself goes through
// the root.
func copyRenameError(root *os.Root, err error) error {
	var le *os.LinkError
	if !errors.As(err, &le) {
		return err
	}
	return &os.LinkError{Op: "rename", Old: filepath.Join(root.Name(), le.Old), New: filepath.Join(root.Name(), le.New), Err: le.Err}
}
