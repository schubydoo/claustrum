//go:build unix

package files

import (
	"io/fs"
	"os"
	"syscall"
)

// readOpenFlag keeps the open of files.read from waiting for the writer of a
// FIFO. 5fd08069 answers a FIFO with no writer in under 3 ms (measured on
// Linux and macOS). The flag does not change the read of a regular file.
const readOpenFlag = syscall.O_NONBLOCK

// NullDevicePath is the path of the null device. A test names another file here.
var NullDevicePath = os.DevNull

// isNullDevice reports whether fi is the null device itself. The test is the
// identity of the file, not its device numbers: 5fd08069 refuses a second device
// node with the numbers of /dev/null (measured on Linux and macOS).
func isNullDevice(fi fs.FileInfo) bool {
	null, err := os.Stat(NullDevicePath)
	return err == nil && os.SameFile(fi, null)
}
