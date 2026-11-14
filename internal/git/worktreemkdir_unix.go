//go:build unix

package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// mkdirWorktreeLeaf creates the final worktree directory component (the caller has
// already made the leading directories). On an unwritable or foreign-owned
// parent, the 7d193f89 failure reads `mkdirat <leaf>: <errno>`. The leaf is the
// basename with no directory. A plain os.Mkdir renders `mkdir <full path>`. That string is wire-visible in
// the mkdir_failed frame, so reproduce the `mkdirat <leaf>` wording by re-wrapping
// the underlying errno with the leaf name. (This file is `//go:build unix`, and
// stdlib syscall.Mkdirat is linux-only, so this rewraps rather than issuing the
// mkdirat syscall. The created directory and the errno are identical.) The leaf asks for mode 0777, and the umask applies. f6010b97 makes
// its leaf with this mode on Linux and macOS, both run at umask 0000.
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
// its symlinks resolved. They match f6010b97 on Linux and macOS:
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
	p := resolvedDir(repo)
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
// <errno>". f6010b97 does the same on Linux and macOS. Not measured: a root made in
// this call, then a failed <directory>. It reads "mkdirat R/cp: <errno>" by the
// same rule. A directory made before a later failure stays.
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

// removeWorktreeLeaf removes the entry at worktreePath: a file, a link as a link,
// or an empty directory. It opens the parent folder and removes the leaf by its name
// through that handle. The first unlinkat has no flag. When it finds a directory,
// a second unlinkat with AT_REMOVEDIR follows. c9125eb8 and 4c111e86 make these
// calls in this order on a failed add (measured on Linux). They do so also for a
// file or a link that took the place of the leaf. macOS runs the same code
// (claustrum's own, not measured). The removal never deletes a directory that
// holds an entry, and it never follows a link. The handle needs no read permission
// on the parent (openLeafParent), as an rmdir by path needs none. undoFailedAdd
// uses it.
//
// parent is the identity of the parent folder that the caller tested, or nil for
// no test. The opened handle must have that identity, or nothing is removed. So a
// parent swapped between the test and the open keeps its entries. This guard is
// claustrum's own (not measured).
func removeWorktreeLeaf(worktreePath string, parent os.FileInfo) error {
	return unlinkWorktreeLeaf(worktreePath, parent, true)
}

// rmdirWorktreeLeaf removes worktreePath only if it is an empty directory, by one
// unlinkat with AT_REMOVEDIR through a handle of its parent. A file or a link at
// the leaf stays. undoFailedAdd uses it when its checkpoint is empty.
func rmdirWorktreeLeaf(worktreePath string) error {
	return unlinkWorktreeLeaf(worktreePath, nil, false)
}

// unlinkWorktreeLeaf is the removal of removeWorktreeLeaf and rmdirWorktreeLeaf.
// anyEntry selects the first unlinkat with no flag.
func unlinkWorktreeLeaf(worktreePath string, parent os.FileInfo, anyEntry bool) error {
	leaf := filepath.Clean(worktreePath)
	if leafParentOpenHook != nil {
		leafParentOpenHook()
	}
	fd, err := openLeafParent(filepath.Dir(leaf))
	if err != nil {
		return &fs.PathError{Op: "open", Path: filepath.Dir(leaf), Err: err}
	}
	defer func() { _ = unix.Close(fd) }()
	if parent != nil && !handleIs(fd, parent) {
		return &fs.PathError{Op: "open", Path: filepath.Dir(leaf), Err: errParentSwapped}
	}
	if leafParentOpenedHook != nil {
		leafParentOpenedHook()
	}
	name := filepath.Base(leaf)
	if anyEntry {
		if err := unix.Unlinkat(fd, name, 0); !unlinkFoundFolder(err) {
			return err
		}
	}
	return unix.Unlinkat(fd, name, unix.AT_REMOVEDIR)
}

// errParentSwapped is the error of removeWorktreeLeaf when the opened parent is not
// the tested one. No reply carries it.
var errParentSwapped = errors.New("the parent folder is not the one that was tested")

// handleIs reports whether the open handle fd has the device and inode of want.
func handleIs(fd int, want os.FileInfo) bool {
	ws, ok := want.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return false
	}
	return st.Dev == ws.Dev && st.Ino == ws.Ino
}

// failedAddLeafResolve is the path that undoFailedAdd compares with the
// checkpoint. A link at the leaf resolves by its parent and keeps its own name, so
// the link itself can go. Any other entry resolves as in leafResolve.
func failedAddLeafResolve(worktreePath string) (string, error) {
	leaf := filepath.Clean(worktreePath)
	if fi, err := os.Lstat(leaf); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return leafResolve(worktreePath)
	}
	parent, err := leafResolve(filepath.Dir(leaf))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(leaf)), nil
}

// heldLeafLacksGitFile reports whether the leaf that the checkpoint holds open has
// no `.git` entry. It reads through the held handle, not the path. A leaf that was
// deleted and replaced by a new folder before the add then has none.
func heldLeafLacksGitFile(cp worktreeCheckpoint) bool {
	if cp.info == nil || len(cp.held) == 0 {
		return false
	}
	rc, err := cp.held[0].SyscallConn()
	if err != nil {
		return false
	}
	var st unix.Stat_t
	var statErr error
	if err := rc.Control(func(fd uintptr) {
		statErr = unix.Fstatat(int(fd), ".git", &st, unix.AT_SYMLINK_NOFOLLOW)
	}); err != nil {
		return false
	}
	return errors.Is(statErr, unix.ENOENT)
}

// leafParentOpenHook is a test seam. It runs in removeWorktreeLeaf before the open
// of the parent. Production never reassigns it.
var leafParentOpenHook func()

// leafParentOpenedHook is a test seam. It runs after removeWorktreeLeaf opens the
// parent of the leaf and before the unlinkat. Production never reassigns it.
var leafParentOpenedHook func()

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
