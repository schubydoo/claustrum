package files

import (
	"os"
	"strconv"
)

// openParentPath reads the link of the handle under /proc/self/fd. A failed read
// gives parent_unverified (c9125eb8, measured on Linux). So a host with no /proc
// answers parent_unverified to every call that opens its parent (claustrum's
// own, not measured).
func openParentPath(fd int) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
}
