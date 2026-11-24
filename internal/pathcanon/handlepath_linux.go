package pathcanon

import (
	"os"
	"strconv"
)

// PathOfOpenFolder is the path of the open folder f as the kernel reports it now. A
// rename of the folder or of a parent shows in the answer.
func PathOfOpenFolder(f *os.File) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
}
