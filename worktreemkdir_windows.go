//go:build windows

package main

import "os"

// mkdirWorktreeLeaf creates the worktree directory. The unix build rewraps its
// error to match the `mkdirat <leaf>` wording of 7d193f89. Windows has no such
// wording pinned, so a plain MkdirAll is sufficient here.
func mkdirWorktreeLeaf(worktreePath string) error {
	return os.MkdirAll(worktreePath, 0o755)
}
