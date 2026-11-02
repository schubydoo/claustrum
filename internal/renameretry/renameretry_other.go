//go:build !windows

package renameretry

import "os"

// Held is false on Linux and macOS: no rename is tried again there.
func Held(error) bool { return false }

// CopyError is the error of a failed rename of the copy write. On Linux
// and macOS it is the error of the root as it is.
func CopyError(_ *os.Root, err error) error { return err }
