//go:build windows

package git

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// walkMayNotLook is false on Windows: the walk of unusableDotGit has no permission
// test there (from the code).
func walkMayNotLook(err error) bool { return false }

// walkMayNotSearch is false on Windows (from the code).
func walkMayNotSearch(g string) bool { return false }

// heldByOtherAccount is false on Windows: the walk has no owner test there (from the
// code).
func heldByOtherAccount(d string) bool { return false }

// repoOfAnotherAccount reports whether the owner of d, the folder of a `.git` folder
// that passes the git-directory test, is another account than the user of the daemon's
// token (walkOtherAccount). A folder whose owner cannot be read counts as its own. So
// does one of the Administrators group while the token is a member of it, as git
// counts it (claustrum's own, not measured). It is a variable, so that a test can stand
// for another account. 87375cc7 and 13047a51 answer unusableGitText for a repository
// folder owned by SYSTEM (measured on Windows). Other owners are not measured.
var repoOfAnotherAccount = func(d string) bool {
	sd, err := windows.GetNamedSecurityInfo(d, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return false
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	if owner.Equals(user.User.Sid) {
		return false
	}
	if owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		if member, err := windows.Token(0).IsMember(owner); err == nil && member {
			return false
		}
	}
	return true
}

// anotherAccountCode is false: notARepoResult answers unusableGitText for
// walkOtherAccount, and 13047a51 has no repository_of_another_account (measured on
// Windows).
const anotherAccountCode = false

// repoOnShare reports whether repo is a path on a network share, in the form
// `\\server\share\...` (shareGitDir). A `\\?\` or `\\.\` path is not. A variable,
// so that a test on another system can run that step.
var repoOnShare = func(repo string) bool {
	v := filepath.VolumeName(repo)
	return isUNC(v) && v[2] != '?' && v[2] != '.'
}
