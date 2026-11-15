//go:build windows

package git

// walkMayNotLook is false on Windows: the walk of unusableDotGit has no permission
// test there (from the code).
func walkMayNotLook(err error) bool { return false }

// walkMayNotSearch is false on Windows (from the code).
func walkMayNotSearch(g string) bool { return false }

// heldByOtherAccount is false on Windows: the walk has no owner test there (from the
// code).
func heldByOtherAccount(d string) bool { return false }
