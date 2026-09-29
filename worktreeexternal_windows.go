//go:build windows

package main

import "path/filepath"

// externalChainCheck returns the <directory> level of worktreePath and no
// refusal. Windows refuses every worktreeRoot before this step runs, so the unix
// checks are not needed here.
func externalChainCheck(worktreeRoot, worktreePath string) (dir, msg, code string) {
	_ = worktreeRoot
	return filepath.Dir(filepath.Clean(worktreePath)), "", ""
}

// externalDirLevelCheck is not needed on Windows for the same reason.
func externalDirLevelCheck(dir string) (msg, code string) {
	_ = dir
	return "", ""
}

// worktreeRootShareRefusal is a no-op on Windows: the uid/gid ownership and
// world/group-writable checks read syscall.Stat_t fields that do not exist on
// Windows, so claustrum accepts an external worktreeRoot on the containment check
// alone here. The reference's Windows behavior (whether it applies a SID/DACL
// equivalent) has not been measured; this matches only the POSIX surface that was.
func worktreeRootShareRefusal(root string) string {
	_ = root
	return ""
}
