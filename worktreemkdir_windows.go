//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// mkdirWorktreeLeaf creates the worktree directory. The unix build rewraps its
// error to match the `mkdirat <leaf>` wording of 7d193f89. Windows has no such
// wording pinned, so a plain MkdirAll is sufficient here.
func mkdirWorktreeLeaf(worktreePath string) error {
	return os.MkdirAll(worktreePath, 0o755)
}

// mkdirRepoWorktreeParents makes the missing directories of dir with os.MkdirAll
// and mode 0755. No probe measured a Windows parent step that fails on a file or on
// a denied create, so it keeps the MkdirAll error text. The unix build walks each
// component to match the texts of f6010b97.
func mkdirRepoWorktreeParents(repo, dir string) error {
	_ = repo
	return os.MkdirAll(dir, 0o755)
}

// mkdirExternalWorktreeParents is never reached on Windows, because Windows
// refuses every worktreeRoot first. It makes dir with os.MkdirAll.
func mkdirExternalWorktreeParents(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

// junctionParentRefusal reports the parent-directory failure of git.worktree_create
// when a directory between repo and the leaf is a junction, or "" when none is. Go
// reports a junction, and any other non-symlink reparse point, as ModeIrregular, not
// as a directory or a symlink, so this refuses those too. The first
// junction, in order from repo, is named as repo joined with its components. The
// reference refuses a junctioned `.claude` and a junctioned `.claude\worktrees` this
// way and creates nothing. Measured against f6010b97 on a Windows VM (rows JCR1 and
// JCR2).
func junctionParentRefusal(repo, worktreePath string) string {
	rel, err := filepath.Rel(repo, filepath.Dir(filepath.Clean(worktreePath)))
	if err != nil || !filepath.IsLocal(rel) {
		return ""
	}
	cur := repo
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return ""
		}
		if fi.Mode()&os.ModeIrregular != 0 {
			return fmt.Sprintf("failed to create parent directory: %s is not a directory", cur)
		}
	}
	return ""
}

// rmdirWorktreeLeaf removes worktreePath only if it is an empty directory.
// RemoveDirectory never deletes a file or a directory that holds an entry, so the
// emptiness test and the removal are one step. undoFailedAdd uses it.
func rmdirWorktreeLeaf(worktreePath string) error {
	p, err := syscall.UTF16PtrFromString(worktreePath)
	if err != nil {
		return err
	}
	return syscall.RemoveDirectory(p)
}

// holdWorktreeDir opens the directory at path and returns the handle, or nil when
// the open fails. checkpointCreatedWorktree holds the leaf and its parent open this
// way until the create answers, as on unix. os.Open cannot be used here: its handle
// shares read and write only, so a held leaf blocks every delete of it.
//
// The handle shares read, write and delete, so git and the rollback can still
// rename or delete the leaf, and the parent can be deleted. Both references hold
// the leaf and its parent open during a create on a Windows VM (handle64). Under
// the handles of f6010b97 a rename of the leaf succeeded, a delete-access open of the parent
// succeeded, and a rename of the parent failed with "Access is denied." while the
// leaf was held inside it. After the leaf was moved out, the parent rename
// succeeded.
//
// File.Stat of the returned handle reads the volume serial and the file index with
// GetFileInformationByHandle when it is called. os.SameFile then compares that
// identity with the one that it reads from the path at check time, so a directory
// that replaced the leaf does not compare equal.
func holdWorktreeDir(path string) *os.File {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil
	}
	return os.NewFile(uintptr(h), path)
}
