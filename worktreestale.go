package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// staleRegistryDir is the folder that the step before the add works on: the
// folder `worktrees` of the common git directory of repo.
//
// A repo with a `.git` folder keeps <repo>/.git/worktrees. A repo with a .git
// file takes the git directory that the file names, relative to repo. If that
// folder holds a commondir file that can be read, the common git directory is
// the folder that the file names (gitDirRegistryDir). If not, it is the git
// directory itself.
//
// With a linked worktree as repo, 5fd08069 removes the stale entry `old` of the
// main repository (cell V9 of round tier0-10, Linux VM, and cell S1W of round
// tier3-02, Linux and macOS VMs). With a separate git directory it removes the
// entry of that directory (cell S1 of round tier3-02, Linux, macOS and Windows
// VMs). With a submodule it removes the entry of .git/modules/<m> (cell S1M,
// Linux VM). Not measured: a commondir file in a git directory that is not an
// entry of a folder `worktrees`.
func staleRegistryDir(repo string) string {
	admin := worktreeAdminDir(repo)
	if admin == "" {
		return filepath.Join(repo, ".git", worktreesSubdir)
	}
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(repo, admin)
	}
	return gitDirRegistryDir(filepath.Clean(admin))
}

// windowsStaleEntries lists the folder dir of root for the Windows step and
// returns, in name order, each entry whose gitdir record names worktreePath,
// with its record. The compare ignores letter case (sameGitDirPath). An entry
// that is no real folder is passed over, so no link and no junction is
// followed (goesAsTree).
func windowsStaleEntries(root *os.Root, dir, worktreePath string) (names []string, records [][]byte) {
	list, err := rootDirNames(root, dir)
	if err != nil {
		return nil, nil
	}
	target := resolveAsFarAsExists(worktreePath)
	for _, name := range list {
		rel := filepath.Join(dir, name)
		if fi, err := root.Lstat(rel); err != nil || !goesAsTree(fi.Mode()) {
			continue
		}
		record, err := readGitPlainFileIn(root, filepath.Join(rel, "gitdir"))
		if err != nil {
			continue
		}
		// The record names the `.git` file of the worktree. Its parent is the worktree.
		if sameGitDirPath(resolveAsFarAsExists(filepath.Dir(strings.TrimSpace(string(record)))), target) {
			names, records = append(names, name), append(records, record)
		}
	}
	return names, records
}

// windowsStaleEntryGoes reports whether the Windows step removes the entry of
// names, which windowsStaleEntries found in the folder dir of root. It goes
// only when it is the one entry there, holds no `locked` file and has no live
// record (staleRecordLive, D27). 5fd08069 keeps an entry with a `locked` file
// and keeps both of two entries, and the add then fails with the text of git
// (cells S1L and S1T of round tier3-05, Windows VM, 3 runs each). Not
// measured: a live record beside a second entry (both stay here), and a
// `locked` test whose Lstat fails with another error than "does not exist"
// (the entry stays).
func windowsStaleEntryGoes(root *os.Root, dir string, names []string, records [][]byte) bool {
	if len(names) != 1 || staleRecordLive(records[0]) {
		return false
	}
	_, err := root.Lstat(filepath.Join(dir, names[0], "locked"))
	return errors.Is(err, fs.ErrNotExist)
}

// staleRecordLive reports whether the path that the gitdir record of a stale
// entry names, as written, still exists (D27). The Windows step keeps such an
// entry. Its compare ignores letter case, so in a folder with case sensitivity
// on, a live worktree W1 matches a create of w1. 5fd08069 deletes the entry of
// W1 there (cell W-CS of round task084-02, Windows VM). The guard is
// claustrum's own, by the maintainer's decision of 2026-10-18. A Lstat that
// fails with another error than "does not exist" keeps the entry too.
func staleRecordLive(record []byte) bool {
	_, err := os.Lstat(strings.TrimSpace(string(record)))
	return !errors.Is(err, fs.ErrNotExist)
}

// resolveAsFarAsExists canonicalises p by resolving the symlinks of its deepest
// existing ancestor and re-attaching the non-existent tail — so a path whose leaf is
// gone (a deleted worktree) still resolves a symlinked ancestor such as macOS
// /var -> /private/var, where canonicalPath alone falls back to the raw path.
func resolveAsFarAsExists(p string) string {
	p = filepath.Clean(p)
	if r := canonicalPath(p); r != p {
		return r // p itself exists and had symlinks to resolve
	}
	dir, base := filepath.Split(p)
	if dir == "" {
		return p
	}
	return filepath.Join(canonicalPath(filepath.Clean(dir)), base)
}
