//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// mkdirWorktreeLeaf creates the final worktree directory component (the caller has
// already made the leading directories). 7d193f89 resolves worktree locations
// through openat/mkdirat, so its failure on an unwritable or foreign-owned parent
// reads `mkdirat <leaf>: <errno>` — the leaf basename with no directory — where a
// plain os.Mkdir would render `mkdir <full path>`. That string is wire-visible in
// the mkdir_failed frame, so reproduce the `mkdirat <leaf>` wording by re-wrapping
// the underlying errno with the leaf name. (stdlib syscall.Mkdirat is linux-only
// and golang.org/x/sys is a Windows-only dependency here, so this rewraps rather
// than issuing the mkdirat syscall; the created directory and the errno are
// identical.)
func mkdirWorktreeLeaf(worktreePath string) error {
	if err := os.Mkdir(worktreePath, 0o755); err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return &fs.PathError{Op: "mkdirat", Path: filepath.Base(worktreePath), Err: pe.Err}
		}
		return err
	}
	return nil
}
