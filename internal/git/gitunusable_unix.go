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
	uid, ok := folderOwner(d)
	return ok && uid != walkOwnUID()
}

// folderOwner is the account that owns the folder d, and false when its stat fails. It
// is a variable, so that a test can stand for another owner of one folder.
var folderOwner = func(d string) (int, bool) {
	fi, err := os.Stat(d)
	if err != nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// repoOfAnotherAccount reports whether d, the folder of a `.git` folder that passes the
// git-directory test, belongs to another account (walkOtherAccount).
func repoOfAnotherAccount(d string) bool { return heldByOtherAccount(d) }

// anotherAccountCode is true: notARepoResult answers repository_of_another_account for
// walkOtherAccount (13047a51, measured on Linux and macOS).
const anotherAccountCode = true

// repoOnShare is false on Linux and macOS: shareGitDir has no step there (from the
// code). A variable, so that a test can run that step.
var repoOnShare = func(repo string) bool { return false }
