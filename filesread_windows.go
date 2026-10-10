//go:build windows

package main

import "io/fs"

// readOpenFlag is 0 on Windows: the open of files.read is a plain os.Open.
const readOpenFlag = 0

// isNullDevice is false on Windows. 5fd08069 refuses NUL in each spelling that
// was measured (Windows VM).
func isNullDevice(fs.FileInfo) bool { return false }
