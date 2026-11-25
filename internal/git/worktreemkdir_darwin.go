package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// mkdirWorktreeLeaf creates the final worktree directory component (the caller has
// already made the leading directories). On an unwritable or foreign-owned
// parent, the 7d193f89 failure reads `mkdirat <leaf>: <errno>`. The leaf is the
// basename with no directory. A plain os.Mkdir renders `mkdir <full path>`. That string is wire-visible in
// the mkdir_failed frame, so reproduce the `mkdirat <leaf>` wording by re-wrapping
// the underlying errno with the leaf name. (The stdlib syscall.Mkdirat is
// linux-only, so this rewraps rather than issuing the mkdirat syscall. The
// created directory and the errno are identical.) The leaf asks for mode 0777, and the umask applies. f6010b97 makes
// its leaf with this mode on Linux and macOS, both run at umask 0000.
func mkdirWorktreeLeaf(worktreePath string) error {
	if err := os.Mkdir(worktreePath, 0o777); err != nil {
		return mkdiratError(worktreePath, err)
	}
	return nil
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
// os.MkdirAll. Linux walks by handles (worktreemkdir_linux.go). On macOS
// 87375cc7 is not measured.
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

// mkdiratError names only the last component of path in err, as `mkdirat <name>:
// <errno>`.
func mkdiratError(path string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return &fs.PathError{Op: "mkdirat", Path: filepath.Base(path), Err: pe.Err}
	}
	return err
}
