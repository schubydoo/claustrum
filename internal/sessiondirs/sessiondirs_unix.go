//go:build unix

package sessiondirs

import (
	"os"
	"syscall"
)

// installHint ends the text of NoOtherGitError on Linux and macOS (measured).
const installHint = "/usr/bin"

// openNoBlock keeps the read of a record from waiting on a FIFO that took the
// place of a regular file (claustrum's own).
const openNoBlock = syscall.O_NONBLOCK

// sameEnvName compares two names of the environment by exact text.
func sameEnvName(a, b string) bool { return a == b }

// warnBare is true: Linux and macOS log the warning of a bare command (measured).
const warnBare = true

// recordResolved is true: Linux and macOS add the resolved path of a session
// folder (withResolved).
const recordResolved = true

// samePath compares two paths by text.
func samePath(a, b string) bool { return a == b }

// folderIdentity is the device and the inode of the folder p, or 0 and 0 when
// its stat fails (claustrum's own, not measured).
func folderIdentity(p string) (dev, ino uint64) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, 0
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return uint64(st.Dev), uint64(st.Ino) //nolint:unconvert // Dev is int32 on macOS
}
