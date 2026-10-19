//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// dropStaleWorktreeRegistration is the step of git.worktree_create before `git
// worktree add`, on Linux and macOS. It works on <repo>/.git/worktrees, or for a
// repo that is a linked worktree on the folder `worktrees` of the main repository
// (staleRegistryDir). An entry of that folder is stale for
// this request if its gitdir record names the .git of the new leaf. The caller runs
// the step only after it confirmed that the leaf does not exist. The step removes
// a stale entry only if it is the only stale entry of the folder and holds no
// `locked` file. The A cells are those of 89cb6289 on Linux and macOS VMs, 2 runs
// each. So are the S cells.
//
// claustrum compares by text. This rule is claustrum's own fit of the cells below:
//
//   - The record is read as a plain file. Blanks and newlines at both ends are cut.
//     A relative record counts from the entry folder. Then the path is cleaned by
//     text only. Its symlinks are not resolved.
//   - The leaf is worktreePath with the symlinks of its existing part resolved,
//     plus "/.git". A record that equals the cleaned worktreePath as sent, plus
//     "/.git", names the leaf too.
//
// With one stale entry, 89cb6289 removes it for these records of the leaf <L>:
// <L>/.git with or without a newline (cells A3 and A4), <L>/.git/ (cells A1 and
// A2), <L>/.git// (cell A5), <L>/.git/. (cell A5b), <L>/.git with a blank after it
// (cell A5c) and the relative path ../../../.claude/worktrees/w1/.git (cell A7).
// The name of the entry does not count: the entry old9 goes too (cell A8). 89cb6289
// keeps the entry for <L> and <L>/, which have no .git part (cells A6 and A6b), and
// for the record of a live worktree at another path (cell A11). A record that
// spells the leaf through a symlink stays for a request with the real path (cell
// A12b). A record with the real path goes for a request through a symlink (cell
// A12c).
//
// A record that spells the leaf as the request does, through a symlink, goes on
// 5fd08069, and the create succeeds: cell i-ctl of val-s3p2b2-11-macos (macOS VM,
// 2 of 2 runs), where /tmp is a symlink to /private/tmp. Cell h-ctl there is the
// control with the real path in the record and in the request. claustrum equals
// 5fd08069 in both cells (val-s3p2b2-13-linux2, Linux VM, and
// val-s3p2b2-13-macos, macOS VM). Not measured: that record on 89cb6289, and a
// request and a record that go through two different symlinks (the entry stays
// here).
//
// With two or three stale entries, 89cb6289 removes none of them: old9 and w1
// (cell A13), old8, old9 and w1 (cell S1), old8 and old9 (cell S2), and two
// entries whose records differ in their spelling (cell S6). With a stale entry and
// a second stale entry that holds a `locked` file, both stay (cells A13b and S5).
// With one stale entry beside an entry that is not stale, the stale entry goes
// (cells S3 and S4). It goes too beside a regular file, a folder with no gitdir
// record or an empty folder (cells S7, S7b and S7c).
//
// An entry that holds a file named `locked` stays (cells A9 and A9b). The remove is
// best effort: it removes what the modes permit, and the request goes on. With the
// entry at mode 0500 only logs/HEAD goes (cell A10). With the registrations folder
// at mode 0555 the files go and the empty folder stays (cell A10b).
//
// A stale entry that stays is not the entry of the new worktree. The create
// refuses later when the add made no entry of its own (createdEntryOnly).
//
// claustrum's own guards: the step opens one os.Root at the registrations folder
// first. It lists the entries, reads each record, tests `locked` and removes
// through that root, so the list, the reads, the `locked` test and the remove use
// one folder. The remove names one direct
// child and follows no symlink. A record that is a symlink out of the registrations
// folder is not read (the rule of os.Root, not measured). An entry that is not
// a real folder is passed over. The home guard runs on the entry path first (D2).
//
// Not measured: a tab or a
// carriage return at an end of the record (cut here), a relative record in a
// repository that is sent through a symlink (it counts from the resolved entry
// folder here), and a `locked` entry whose stat fails with another error than "does
// not exist" (the entry stays).
//
// held is the git directory that the create holds, or nil. When base is its
// folder `worktrees`, the root is opened through the held folder. A base that
// is a symbolic link is not opened, and nothing is removed
// (acceptedGitDir.deleteRoot).
//
// matched reports that an entry is stale, also one that stays (pairBeforeAdd).
// With two stale entries 5fd08069 removes neither and runs no pair (cell S2X
// of round task084-02, Linux and macOS VMs).
func dropStaleWorktreeRegistration(repo, worktreePath string, held *acceptedGitDir) (matched bool) {
	base := staleRegistryDir(repo)
	root, err := held.deleteRoot(base)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return false
	}
	ents, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return false
	}
	// The order of os.ReadDir, which this step had before it used a root.
	slices.SortFunc(ents, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	target := filepath.Join(canonicalPathOfGone(worktreePath), ".git")
	sent := filepath.Join(worktreePath, ".git")
	var stale []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		entry := filepath.Join(base, e.Name())
		// A record that is not a regular file is passed over at once. With a FIFO
		// there, 89cb6289 goes on to `git worktree add` (row B-G1, Linux and macOS VMs).
		b, err := readGitPlainFileIn(root, e.Name()+"/gitdir")
		if err != nil {
			continue
		}
		record := strings.TrimSpace(string(b))
		if !filepath.IsAbs(record) {
			record = filepath.Join(canonicalPath(entry), record)
		}
		if named := filepath.Clean(record); named == target || named == sent {
			stale = append(stale, e.Name())
		}
	}
	// Two or more stale entries: every one stays (cells A13, S1, S2 and S5).
	if len(stale) == 1 {
		removeStaleRegistration(root, stale[0], filepath.Join(base, stale[0]))
	}
	return len(stale) > 0
}

// staleRegistryDir is the folder that the step before the add works on.
//
// With a repo that is a linked worktree, 5fd08069 removes the stale entry `old`
// of the main repository and creates the worktree (cell V9 of round tier0-10,
// Linux VM, and cell V9c for a main repository). claustrum takes repo as a linked
// worktree when its .git file names a folder whose parent is named `worktrees`
// and that folder holds a commondir file that can be read. The folder of the
// step is then `worktrees` of the common git directory that the file names.
//
// Every other repo keeps <repo>/.git/worktrees. With a .git file of another
// kind, such as that of a submodule or of a separate git directory, that path
// does not open and the step does nothing (not measured).
func staleRegistryDir(repo string) string {
	base := filepath.Join(repo, ".git", worktreesSubdir)
	admin := worktreeAdminDir(repo)
	if admin == "" {
		return base
	}
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(repo, admin)
	}
	admin = filepath.Clean(admin)
	if filepath.Base(filepath.Dir(admin)) != worktreesSubdir {
		return base
	}
	b, err := readGitPlainFile(filepath.Join(admin, "commondir"))
	if err != nil {
		return base
	}
	common := strings.TrimSpace(string(b))
	if !filepath.IsAbs(common) {
		common = filepath.Join(admin, common)
	}
	return filepath.Join(common, worktreesSubdir)
}

// removeStaleRegistration removes the entry name of root, the opened registrations
// folder, unless it holds a `locked` file. entry is the path of the entry, for the
// home guard.
func removeStaleRegistration(root *os.Root, name, entry string) {
	if _, err := root.Lstat(name + "/locked"); errors.Is(err, fs.ErrNotExist) && !wipesHomeDir(entry) {
		_ = removeHeldEntry(root, name)
	}
}
