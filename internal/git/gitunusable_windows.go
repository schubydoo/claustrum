//go:build windows

package git

import "path/filepath"

// walkMayNotLook is false on Windows: the walk of unusableDotGit has no permission
// test there (from the code).
func walkMayNotLook(err error) bool { return false }

// walkMayNotSearch is false on Windows (from the code).
func walkMayNotSearch(g string) bool { return false }

// heldByOtherAccount is false on Windows: the walk has no owner test there (from the
// code).
func heldByOtherAccount(d string) bool { return false }

// repoOnShare reports whether repo is a path on a network share, in the form
// `\\server\share\...` (shareGitDir). A `\\?\` or `\\.\` path is not. A variable,
// so that a test on another system can run that step.
var repoOnShare = func(repo string) bool {
	v := filepath.VolumeName(repo)
	return isUNC(v) && v[2] != '?' && v[2] != '.'
}
