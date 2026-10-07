//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// dropStaleWorktreeRegistration is the step of git.worktree_create before `git
// worktree add`, on Linux and macOS. It removes the entry of <repo>/.git/worktrees
// whose gitdir record names the .git of the new leaf. The caller runs it only after
// it confirmed that the leaf does not exist, so such an entry is stale. The cells
// are those of 89cb6289 on a Linux VM with git 2.43 (cells A1 to A12c, 2 runs
// each). No macOS VM ran them.
//
// claustrum compares by text. This rule is claustrum's own fit of the cells below:
//
//   - The record is read as a plain file. Blanks and newlines at both ends are cut.
//     A relative record counts from the entry folder. Then the path is cleaned by
//     text only. Its symlinks are not resolved.
//   - The leaf is worktreePath with the symlinks of its existing part resolved,
//     plus "/.git".
//
// 89cb6289 removes the entry for these records of the leaf <L>: <L>/.git with or
// without a newline (cells A3 and A4), <L>/.git/ (cells A1 and A2), <L>/.git//
// (cell A5), <L>/.git/. (cell A5b), <L>/.git with a blank after it (cell A5c) and
// the relative path ../../../.claude/worktrees/w1/.git (cell A7). The name of the
// entry does not count: the entry old9 goes too (cell A8). 89cb6289 keeps the entry
// for <L> and <L>/, which have no .git part (cells A6 and A6b), and for the record
// of a live worktree at another path (cell A11). A record that spells the leaf
// through a symlink stays for a request with the real path (cell A12b). A record
// with the real path goes for a request through a symlink (cell A12c).
//
// An entry that holds a file named `locked` stays (cells A9 and A9b). The remove is
// best effort: it removes what the modes permit, and the request goes on. With the
// entry at mode 0500 only logs/HEAD goes (cell A10). With the registrations folder
// at mode 0555 the files go and the empty folder stays (cell A10b).
//
// kept holds the path of each stale entry that is still there after this step.
// The create refuses later if the registration of the new worktree is one of them
// (staleRegistrationRefusal).
//
// claustrum's own guards: the remove goes through an os.Root at the registrations
// folder and names one direct child, so it follows no symlink. An entry that is not
// a real folder is passed over. The home guard runs on the entry path first (D2).
//
// Not measured: more than one stale entry. claustrum handles every one by the
// rules above, and remembers every one that stays. If the step ended at the first
// match, a second stale entry got the index of the new worktree. Not measured either: a tab or a
// carriage return at an end of the record (cut here), a relative record in a
// repository that is sent through a symlink (it counts from the resolved entry
// folder here), and a `locked` entry whose stat fails with another error than "does
// not exist" (the entry stays).
func dropStaleWorktreeRegistration(repo, worktreePath string) (kept []string) {
	base := filepath.Join(repo, ".git", worktreesSubdir)
	ents, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	target := filepath.Join(canonicalPathOfGone(worktreePath), ".git")
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		entry := filepath.Join(base, e.Name())
		// A record that is not a regular file is passed over at once. With a FIFO
		// there, 89cb6289 goes on to `git worktree add` (row B-G1, Linux and macOS VMs).
		b, err := readGitPlainFile(filepath.Join(entry, "gitdir"))
		if err != nil {
			continue
		}
		record := strings.TrimSpace(string(b))
		if !filepath.IsAbs(record) {
			record = filepath.Join(canonicalPath(entry), record)
		}
		if filepath.Clean(record) != target {
			continue
		}
		if stays := removeStaleRegistration(base, e.Name()); stays != "" {
			kept = append(kept, stays)
		}
	}
	return kept
}

// removeStaleRegistration removes the entry name of the registrations folder base,
// unless it holds a `locked` file. It returns the path of the entry if the entry is
// still there afterwards, or "".
func removeStaleRegistration(base, name string) (kept string) {
	entry := filepath.Join(base, name)
	root, err := os.OpenRoot(base)
	if err != nil {
		return entry
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat(name + "/locked"); errors.Is(err, fs.ErrNotExist) && !wipesHomeDir(entry) {
		_ = root.RemoveAll(name)
	}
	if _, err := root.Lstat(name); errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return entry
}
