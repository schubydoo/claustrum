//go:build unix

package git

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/schubydoo/claustrum/internal/homeguard"
	"github.com/schubydoo/claustrum/internal/pathcanon"
)

// staleStepAfterGitDir is false on Linux and macOS. There the create runs the
// stale step before it makes the parent folders and the leaf.
const staleStepAfterGitDir = false

// unlockQuote quotes the path of a `git worktree unlock` hint. Linux and macOS
// use single quotes (be1c1ca5, measured on Linux and macOS).
const unlockQuote = "'"

// genericHintChars are the characters of a leaf path that give the unlock hint with
// no path: `$` (measured on Linux and macOS), `'` and a backslash (measured on
// Linux).
const genericHintChars = "$'\\"

// removeStaleWorktreeEntry is the step of git.worktree_create before `git
// worktree add`, on Linux and macOS. It works on the folder `worktrees` of the
// common git directory of repo (staleRegistryDir). An entry of that folder is stale for
// this request if its gitdir record names the .git of the new leaf. The caller runs
// the step only after it confirmed that the leaf does not exist. The step removes
// a stale entry only if it is the only stale entry of the folder and holds no
// `locked` file. The A cells are those of 89cb6289 on Linux and macOS, 2 runs each. So
// are the S cells.
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
// <L>/.git with or without a newline (measured), <L>/.git/, <L>/.git//, <L>/.git/.
// (measured), <L>/.git with a blank after it and the relative path
// ../../../.claude/worktrees/w1/.git. The name of the entry does not count: the
// entry old9 goes too (measured). 89cb6289 keeps the entry for <L> and <L>/, which
// have no .git part (measured), and for the record of a live worktree at another
// path. A record that spells the leaf through a symlink stays for a request with
// the real path (measured). A record with the real path goes for a request through
// a symlink (measured).
//
// A record that spells the leaf as the request does, through a symlink, goes on
// 5fd08069, and the create succeeds: measured on macOS (2 of 2 runs), where
// /tmp is a symlink to /private/tmp. A control there has the real path in the
// record and in the request. claustrum equals 5fd08069 in both cases (measured
// on Linux and macOS). Not measured: that record on 89cb6289, and a request and
// a record that go through two different symlinks (the entry stays here).
//
// With two or three stale entries, 89cb6289 removes none of them: old9 and w1
// (measured), old8, old9 and w1, old8 and old9, and two entries whose records
// differ in their spelling. With a stale entry and a second stale entry that holds
// a `locked` file, both stay (measured). With one stale entry beside an entry that
// is not stale, the stale entry goes (measured). It goes too beside a regular
// file, a folder with no gitdir record or an empty folder (measured).
//
// An entry that holds a file named `locked` stays (measured). The remove is best
// effort: it removes what the modes permit, and the request goes on. With the entry
// at mode 0500 only logs/HEAD goes (measured). With the registrations folder at
// mode 0555 the files go and the empty folder stays (measured).
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
// With two stale entries 5fd08069 removes neither and runs no pair (measured
// on Linux and macOS). removed reports that the step removed the one stale
// entry.
func removeStaleWorktreeEntry(repo, worktreePath string, held *acceptedGitDir) (matched, removed bool) {
	base := staleRegistryDir(repo)
	root, err := held.deleteRoot(base)
	if err != nil {
		return false, false
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return false, false
	}
	ents, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return false, false
	}
	// The order of os.ReadDir, which this step had before it used a root.
	slices.SortFunc(ents, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	target := filepath.Join(pathcanon.OfGone(worktreePath), ".git")
	sent := filepath.Join(worktreePath, ".git")
	var stale []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		entry := filepath.Join(base, e.Name())
		// A record that is not a regular file is passed over at once. With a FIFO
		// there, 89cb6289 goes on to `git worktree add` (measured on Linux and macOS).
		b, err := readGitPlainFileIn(root, e.Name()+"/gitdir")
		if err != nil {
			continue
		}
		record := strings.TrimSpace(string(b))
		if !filepath.IsAbs(record) {
			record = filepath.Join(pathcanon.Canonical(entry), record)
		}
		if named := filepath.Clean(record); named == target || named == sent {
			stale = append(stale, e.Name())
		}
	}
	// Two or more stale entries: every one stays (measured).
	if len(stale) == 1 {
		removed = removeStaleRegistration(root, stale[0], filepath.Join(base, stale[0]))
	}
	return len(stale) > 0, removed
}

// removeStaleRegistration removes the entry name of root, the opened registrations
// folder, unless it holds a `locked` file. entry is the path of the entry, for the
// home guard. It reports that the remove ran and gave no error.
func removeStaleRegistration(root *os.Root, name, entry string) bool {
	if _, err := root.Lstat(name + "/locked"); errors.Is(err, fs.ErrNotExist) && !homeguard.WipesHomeDir(entry) {
		return removeHeldEntry(root, name) == nil
	}
	return false
}
