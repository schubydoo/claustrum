//go:build !windows

package main

// gitInfoRoot is the root of git.info. On Linux and macOS it is the walk root itself,
// and no git call runs for it (gitWalkRoot).
func gitInfoRoot(_ *gitStop, walkRoot, _, _ string, _, _ []string) string {
	return walkRoot
}

// existingPathSpelling is the path of the `already exists` refusal of
// git.worktree_create without worktreeRoot. Off Windows it is the cleaned path with
// the symlinks of its parent folder resolved and its last name kept
// (parentResolvedPath, Linux and macOS).
func existingPathSpelling(p string) string {
	return parentResolvedPath(p)
}

// externalPathSpelling is the path that two refusals of git.worktree_create with a
// worktreeRoot name: the leaf in the `already exists` text, and the folder that
// holds the leaf in the "is not marked as a worktree directory" text. Off Windows
// it is the spelling of parentResolvedPath (measured on Linux and macOS). For a path with
// no symlink that is the cleaned path, as f6010b97 and 90fca6e6 name it (measured on Linux
// and macOS).
func externalPathSpelling(p string) string {
	return parentResolvedPath(p)
}

// checkoutWorkTree is the --work-tree value of the read-tree checkout of
// git.worktree_create: the leaf with its symlinks resolved, as the checkpoint of the
// create read it. f6010b97 and 89cb6289 pass the resolved leaf on Linux and macOS
// (measured). Those measurements are creates without worktreeRoot. A symlinked
// worktreeRoot is not measured. Without a checkpoint it is the leaf as sent.
func checkoutWorkTree(leaf, resolved string) string {
	if resolved == "" {
		return leaf
	}
	return resolved
}

// adminRecordChecked reports whether git.worktree_create compares the new worktree's
// admin record with worktreePath after symlink resolution (adminRecordMismatch). It
// does on Linux and macOS. Both references refuse a mismatch on macOS (measured). That
// case is not measured on Linux. Other measurements show the record test on Linux
// (adminRecordMismatch).
const adminRecordChecked = true
