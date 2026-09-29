//go:build unix

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
// identical.) The leaf asks for mode 0777, and the umask applies. f6010b97 makes
// its leaf with this mode on Linux and macOS VMs, both run at umask 0000.
func mkdirWorktreeLeaf(worktreePath string) error {
	if err := os.Mkdir(worktreePath, 0o777); err != nil {
		return mkdiratError(worktreePath, err)
	}
	return nil
}

// mkdiratError names only the last component of path in err, as `mkdirat <name>:
// <errno>`.
func mkdiratError(path string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return &fs.PathError{Op: "mkdirat", Path: filepath.Base(path), Err: pe.Err}
	}
	return err
}

// mkdirRepoWorktreeParents makes the missing directories between repo and dir, top
// first, for a create without worktreeRoot. A new directory asks for mode 0755, and
// the umask applies. An existing directory keeps its mode. The texts use repo with
// its symlinks resolved. They match f6010b97 on Linux and macOS VMs:
//   - An existing component that is not a directory: "<path> is not a directory".
//   - An existing directory without search permission: "statat .: <errno>". This
//     includes the last directory when nothing is left to create.
//   - A failed create names that component only: "mkdirat <name>: <errno>".
//
// A directory made before a later failure stays, as on f6010b97. A component that
// another process creates at the same moment passes if it is a directory, as in
// os.MkdirAll.
func mkdirRepoWorktreeParents(repo, dir string) error {
	rel, err := filepath.Rel(filepath.Clean(repo), dir)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		// The containment refusal keeps dir inside repo, so only dir == repo gets here.
		return nil
	}
	p := resolvedPath(repo)
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, name)
		if fi, err := os.Stat(p); err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("%s is not a directory", p)
			}
			if _, err := os.Stat(p + "/."); err != nil {
				return &fs.PathError{Op: "statat", Path: ".", Err: pathErrno(err)}
			}
			continue
		}
		if err := os.Mkdir(p, 0o755); err != nil {
			if fi, serr := os.Stat(p); serr == nil && fi.IsDir() {
				continue
			}
			return mkdiratError(p, err)
		}
	}
	return nil
}

// mkdirExternalWorktreeParents makes the missing directories of dir, the resolved
// <directory> level of a create with worktreeRoot. Each new directory asks for mode
// 0700, and the umask applies. A failed create names the path relative to the
// deepest directory that existed before the call, for example "mkdirat a/b:
// <errno>". f6010b97 does the same on Linux and macOS VMs. Not measured: a root
// made in this call, then a failed <directory>. It reads "mkdirat R/cp: <errno>" by
// the same rule. A directory made before a later failure stays.
func mkdirExternalWorktreeParents(dir string) error {
	base := dir
	var missing []string
	for {
		if _, err := os.Lstat(base); err == nil {
			break
		}
		up := filepath.Dir(base)
		if up == base {
			break
		}
		missing = append([]string{filepath.Base(base)}, missing...)
		base = up
	}
	p := base
	for i, name := range missing {
		p = filepath.Join(p, name)
		if err := os.Mkdir(p, 0o700); err != nil {
			if fi, serr := os.Stat(p); serr == nil && fi.IsDir() {
				continue
			}
			return &fs.PathError{Op: "mkdirat", Path: filepath.Join(missing[:i+1]...), Err: pathErrno(err)}
		}
	}
	return nil
}

// resolvedPath is p with its symlinks resolved, or p cleaned when that fails.
func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// pathErrno is the error inside a *fs.PathError, or err itself.
func pathErrno(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// junctionParentRefusal is a Windows check. Unix has no junctions, and a symlinked
// component is refused earlier (worktreeSymlinkRefusal).
func junctionParentRefusal(repo, worktreePath string) string {
	_, _ = repo, worktreePath
	return ""
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
