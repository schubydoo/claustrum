package git

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// mkdirWorktreeLeaf creates the final worktree directory component (the caller has
// already made the leading directories). It opens the parent folder as a path
// handle and makes the leaf through it with mode 0777, less the umask. 87375cc7
// makes the leaf by a mkdirat through a handle of its parent (measured on Linux).
// A failure reads `mkdirat <leaf>: <errno>`, also when the parent cannot be
// opened. The errno is that of a mkdir by path (claustrum's own). That string is
// wire-visible in the mkdir_failed frame (7d193f89, measured).
func mkdirWorktreeLeaf(worktreePath string) error {
	leaf := filepath.Clean(worktreePath)
	fd, err := openLeafParent(filepath.Dir(leaf))
	if err != nil {
		return &fs.PathError{Op: "mkdirat", Path: filepath.Base(leaf), Err: err}
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Mkdirat(fd, filepath.Base(leaf), 0o777); err != nil {
		return &fs.PathError{Op: "mkdirat", Path: filepath.Base(leaf), Err: err}
	}
	return nil
}

// mkdirRepoWorktreeParents makes the missing directories between repo and dir, top
// first, for a create without worktreeRoot. It opens repo, with its symlinks
// resolved, and walks down by handles. Each folder is tested and made by name
// through the handle of its parent, then opened. 87375cc7 does the same (measured
// on Linux). A new directory asks for mode 0755, and the umask applies. An existing
// directory keeps its mode. The texts of 87375cc7 (measured on Linux):
//   - An existing component that is not a directory: "<path> is not a directory".
//     <path> is its full path, with the symlinks of repo resolved.
//   - A directory that cannot be opened: "openat <name>: <errno>".
//   - A directory without search permission: "\"<name>\" could not be examined
//     (statat .: <errno>)". This includes the last directory.
//   - A failed create names that component only: "mkdirat <name>: <errno>".
//
// A directory made before a later failure stays. A component that another process
// creates at the same moment passes if it is a directory, as in os.MkdirAll. A
// link there gets the text of a non-directory (claustrum's own, not measured).
func mkdirRepoWorktreeParents(repo, dir string) error {
	rel, err := filepath.Rel(filepath.Clean(repo), dir)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		// The containment refusal keeps dir inside repo, so only dir == repo gets here.
		return nil
	}
	p := resolvedDir(repo)
	names := strings.Split(rel, string(filepath.Separator))
	// A repo that cannot be opened names the first component, as a mkdir by
	// path did before (claustrum's own, not measured).
	fd, err := retryEINTR(func() (int, error) {
		return unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return &fs.PathError{Op: "mkdirat", Path: names[0], Err: err}
	}
	defer func() { _ = unix.Close(fd) }()
	for _, name := range names {
		p = filepath.Join(p, name)
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
			if st.Mode&unix.S_IFMT != unix.S_IFDIR {
				return fmt.Errorf("%s is not a directory", p)
			}
		} else if err := unix.Mkdirat(fd, name, 0o755); err != nil {
			if serr := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); serr != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
				return &fs.PathError{Op: "mkdirat", Path: name, Err: err}
			}
		}
		next, err := retryEINTR(func() (int, error) {
			return unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		})
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return fmt.Errorf("%s is not a directory", p)
		}
		if err != nil {
			return &fs.PathError{Op: "openat", Path: name, Err: err}
		}
		_ = unix.Close(fd)
		fd = next
		if parentWalkHook != nil {
			parentWalkHook(name)
		}
		if err := unix.Fstatat(fd, ".", &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("%q could not be examined (%v)", name, &fs.PathError{Op: "statat", Path: ".", Err: err})
		}
	}
	return nil
}

// parentWalkHook is a test seam. It runs in mkdirRepoWorktreeParents after the
// open of each folder. Production never reassigns it.
var parentWalkHook func(name string)
