//go:build !windows

package main

// gitInfoRoot is the root of git.info. On Linux and macOS it is the walk root itself,
// and no git call runs for it (gitWalkRoot).
func gitInfoRoot(walkRoot, _ string, _ []string) string {
	return walkRoot
}

// existingPathSpelling is the path of the `already exists` refusal of
// git.worktree_create. Off Windows it is the path as sent.
func existingPathSpelling(p string) string {
	return p
}

// checkoutWorkTree is the --work-tree value of the read-tree checkout of
// git.worktree_create: the leaf with its symlinks resolved, as the checkpoint of the
// create read it. f6010b97 and 89cb6289 pass the resolved leaf on Linux and macOS VMs
// (rows D02, I01c, I01e and CSc). Those rows are creates without worktreeRoot. A
// symlinked worktreeRoot is not measured. Without a checkpoint it is the leaf as sent.
func checkoutWorkTree(leaf, resolved string) string {
	if resolved == "" {
		return leaf
	}
	return resolved
}

// adminRecordChecked reports whether git.worktree_create compares the new worktree's
// admin record with worktreePath after symlink resolution (adminRecordMismatch). It
// does on Linux and macOS. Both references refuse a mismatch on a macOS VM (row
// I07a). Linux is not measured, and claustrum checks there too.
const adminRecordChecked = true
