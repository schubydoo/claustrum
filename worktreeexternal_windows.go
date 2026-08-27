//go:build windows

package main

// worktreeRootShareRefusal is a no-op on Windows: the uid/gid ownership and
// world/group-writable checks read syscall.Stat_t fields that do not exist on
// Windows, so claustrum accepts an external worktreeRoot on the containment check
// alone here. The reference's Windows behavior (whether it applies a SID/DACL
// equivalent) has not been measured; this matches only the POSIX surface that was.
func worktreeRootShareRefusal(root string) string {
	_ = root
	return ""
}
