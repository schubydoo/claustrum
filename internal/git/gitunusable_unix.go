//go:build unix

package git

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// walkOwnUID is the account that heldByOtherAccount compares with the owner of a
// folder. It is a variable so that a test can stand for another account.
var walkOwnUID = os.Geteuid

// walkMayNotLook reports whether err, of the stat of a `.git` on the walk of
// unusableDotGit, comes from a folder that the daemon cannot search.
func walkMayNotLook(err error) bool { return errors.Is(err, fs.ErrPermission) }

// walkMayNotSearch reports whether g, a `.git` folder that the walk found, is one
// that the daemon cannot search.
func walkMayNotSearch(g string) bool { return gitDirUnsearchable(g) }

// heldByOtherAccount reports whether the folder d belongs to another account than the
// daemon. A folder that cannot be stat'ed counts as its own (not measured).
func heldByOtherAccount(d string) bool {
	fi, err := os.Stat(d)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) != walkOwnUID()
}
