package files

import (
	"os"
	"strconv"
)

// openParentPath reads the link of the handle under /proc/self/fd (c9125eb8,
// measured on Linux). A failed read gives parent_unverified, so a host with no
// /proc answers parent_unverified to every call (claustrum's own, not measured).
func openParentPath(fd int) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
}
