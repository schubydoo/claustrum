//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// mkdirWorktreeLeaf creates the final worktree directory component (the caller has
// already made the leading directories). On an unwritable or foreign-owned
// parent, the 7d193f89 failure reads `mkdirat <leaf>: <errno>`. The leaf is the
// basename with no directory. A plain os.Mkdir renders `mkdir <full path>`. That string is wire-visible in
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

// rmdirWorktreeLeaf removes worktreePath only if it is an empty directory. rmdir(2)
// never deletes a file or a directory that holds an entry, so the emptiness test and
// the removal are one step. undoFailedAdd uses it.
func rmdirWorktreeLeaf(worktreePath string) error {
	return syscall.Rmdir(worktreePath)
}

// holdWorktreeDir opens the directory at path and returns the handle, or nil when
// the open fails. checkpointCreatedWorktree holds the leaf and its parent open this
// way until the create answers. An open handle keeps a deleted directory's inode
// allocated, so a new directory at the same path gets a new inode number. A held
// handle does not stop a delete on unix.
func holdWorktreeDir(path string) *os.File {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	return f
}
