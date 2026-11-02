//go:build windows

package files

import "io/fs"

// readOpenFlag is 0 on Windows: the open of files.read is a plain os.Open.
const readOpenFlag = 0

// isNullDevice is false on Windows. 5fd08069 refuses NUL in each spelling that
// was measured on Windows.
func isNullDevice(fs.FileInfo) bool { return false }
