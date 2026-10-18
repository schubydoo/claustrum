//go:build !windows

package main

import "os"

// renameHeld is false on Linux and macOS: no rename is tried again there.
func renameHeld(error) bool { return false }

// copyRenameError is the error of a failed rename of the copy write. On Linux
// and macOS it is the error of the root as it is.
func copyRenameError(_ *os.Root, err error) error { return err }
