//go:build linux || darwin

package main

import (
	"cmp"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// stagedModeBits are the bits of a mode that the staging step copies.
const stagedModeBits = fs.ModePerm | fs.ModeSetgid | fs.ModeSticky

// stagedEntryModes gives the entries of the staging folder staging in git,
// the root at the git directory, the modes that git gave their sources in
// reg, the root at the registration in `.s<n>`. The maintainer chose this
// rule to give the measured modes (claustrum's own):
//
//   - On Linux be1c1ca5 gives the new entry the mode 2775 and the group of
//     `worktrees` (mode 2775 there), and `HEAD`, `commondir`, `gitdir` and
//     `index` the mode 664 with that group, in a `--shared=group`
//     repository. The staging folder takes the mode of the source folder.
//     Where `worktrees` has the set-group-ID bit, each entry takes its group.
//   - On macOS be1c1ca5 gives the new entry `drwxr-xr-x` and the group of
//     `.git`, `HEAD` `-rw-rw-r--`, `commondir` and `gitdir` `-rw-r--r--`, and
//     `refs` `drwxrwsr-x` (measured on macOS). The staging folder keeps the
//     mode of its mkdir there, and no group changes.
//
// A repository with no shared modes is not measured. A step that fails logs
// one line, and the create goes on (claustrum's own).
func stagedEntryModes(reg, git *os.Root, staging string) {
	if stagedEntryTakesWorktreesGroup {
		if fi, err := git.Stat(worktreesSubdir); err == nil && fi.Mode()&fs.ModeSetgid != 0 {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				if err := chownStagedTree(git, staging, ".", int(st.Gid)); err != nil {
					logWarnf("[GitHandler] the group of the new worktree's entry was not set: %v", err)
				}
			}
		}
		if fi, err := reg.Stat("."); err == nil {
			if err := git.Chmod(staging, fi.Mode()&stagedModeBits); err != nil {
				logWarnf("[GitHandler] the mode of the new worktree's entry was not set: %v", err)
			}
		}
	}
	if err := copyStagedModes(reg, git, staging, ""); err != nil {
		logWarnf("[GitHandler] the modes of the new worktree's files were not set: %v", err)
	}
}

// copyStagedModes gives each entry below the folder rel of base in dst the
// mode of the same entry of src.
func copyStagedModes(src, dst *os.Root, base, rel string) error {
	names, err := rootDirNames(src, cmp.Or(rel, "."))
	if err != nil {
		return err
	}
	for _, n := range names {
		path := filepath.Join(rel, n)
		fi, err := src.Lstat(path)
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() && !goesAsTree(fi.Mode()) {
			continue
		}
		if err := dst.Chmod(filepath.Join(base, path), fi.Mode()&stagedModeBits); err != nil {
			return err
		}
		if goesAsTree(fi.Mode()) {
			if err := copyStagedModes(src, dst, base, path); err != nil {
				return err
			}
		}
	}
	return nil
}

// chownStagedTree gives the folder rel of base in root, and each entry below
// it, the group gid. Nothing follows a link.
func chownStagedTree(root *os.Root, base, rel string, gid int) error {
	path := filepath.Join(base, rel)
	if err := root.Lchown(path, -1, gid); err != nil {
		return err
	}
	fi, err := root.Lstat(path)
	if err != nil || !goesAsTree(fi.Mode()) {
		return err
	}
	names, err := rootDirNames(root, path)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := chownStagedTree(root, base, filepath.Join(rel, n), gid); err != nil {
			return err
		}
	}
	return nil
}
