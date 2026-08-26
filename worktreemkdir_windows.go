//go:build windows

package main

import "os"

// mkdirWorktreeLeaf creates the worktree directory. The unix build uses
// openat/mkdirat to match 7d193f89's `mkdirat <leaf>` error wording; Windows has no
// such wording pinned, so a plain MkdirAll is sufficient here.
func mkdirWorktreeLeaf(worktreePath string) error {
	return os.MkdirAll(worktreePath, 0o755)
}
