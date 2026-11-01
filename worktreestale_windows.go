//go:build windows

package main

import (
	"os"
	"path/filepath"
)

// staleStepAfterGitDir is true on Windows. There the create runs the stale
// step after `rev-parse --absolute-git-dir`, when the leaf folder already exists.
// 5fd08069 removes the entry at that point (cell S1 of round tier3-02 and cell
// D1K of round tier3-05, Windows VM, 3 runs each).
const staleStepAfterGitDir = true

// unlockQuote quotes the path of a `git worktree unlock` hint. Windows uses double
// quotes (be1c1ca5, measured on Windows).
const unlockQuote = `"`

// genericHintChars are the characters of a leaf path that give the unlock hint with
// no path: `$` (measured on Windows) and `'` (not measured on Windows).
const genericHintChars = "$'"

// dropStaleWorktreeRegistration removes the repo's worktree registration for
// worktreePath when that path is registered but missing on disk — a "prunable"
// registration left when a session folder is deleted out from under git. Without
// this, `git worktree add` at the same path fails "missing but already registered"
// where 7d193f89 prunes the stale record and recreates cleanly (not a Windows
// measurement).
//
// Only the registration for THIS path is dropped; other prunable registrations are
// left in place (measured against 7d193f89, not a Windows measurement — a global `git worktree prune` would
// clear those too and diverge). The leaf already exists when this step
// runs (staleStepAfterGitDir). The caller confirmed before that
// the path did not exist, so a matching registration is stale.
//
// A registration lives at <common git dir>/worktrees/<name>/gitdir and points at
// "<worktree>/.git" (staleRegistryDir). Both paths are compared through
// resolveAsFarAsExists, so a symlinked ancestor matches. Best-effort: any failure
// leaves git to report its own error. 5fd08069 removes the entry of a separate
// git directory in cell S1 of round tier3-02 (Windows VM). A linked worktree as
// repo is not measured on Windows, and neither is a relative record.
//
// The entry goes only when it is the one entry that names the leaf and holds no
// `locked` file (windowsStaleEntryGoes, cells S1L and S1T of round tier3-05).
// The folder of the step must be a real folder by its Lstat: a junction or a
// directory symbolic link there gets no listing and no delete. With such a link
// at `.git\worktrees`, a stale entry behind it and "shadowDirOff":true,
// 5fd08069 changes nothing behind the link (cells D1J and D1S of round
// tier3-05, Windows VM, 3 runs each). It then refuses the create with the text
// of offWorktreesRefusal. The same Lstat test runs in the state shadow, where
// such a link is not measured (claustrum's own).
//
// held is the git directory that the create holds, or nil. When the common git
// directory of repo is the held folder (acceptedGitDir.isAt), the step goes
// through it (acceptedGitDir.dropStaleRegistration) and lists, reads and
// removes nothing by the path. Every other request gets the step by the path:
// a request with "shadowDirOff":true, the state fallback-git, a server with no
// run folder, and a held folder that is not that directory, such as a daemon
// GIT_DIR of another repository (not measured). By the path the step opens one
// os.Root at the folder and tests that it is the folder of the Lstat. The list,
// the reads, the `locked` test and the remove go through that root. The remove
// runs wipesHomeDir on the entry first (D2). The root is claustrum's own, not
// measured.
//
// The compare ignores letter case (sameGitDirPath). 5fd08069 removes a stale
// entry whose record differs from the leaf only in letter case, in one name or
// in the whole path (cells R-Q4i and R-Q4i2 of round task084-01, Windows VM).
// A create that holds no git directory is not measured there. An entry whose
// record names a path that exists stays (staleRecordLive, D27).
//
// matched reports that an entry names the leaf, also one that stays
// (pairBeforeAdd).
func dropStaleWorktreeRegistration(repo, worktreePath string, held *acceptedGitDir) (matched bool) {
	base := staleRegistryDir(repo)
	if held.isAt(filepath.Dir(base)) {
		return held.dropStaleRegistration(worktreePath)
	}
	fi, err := os.Lstat(base)
	if err != nil || !goesAsTree(fi.Mode()) {
		return false
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	if !sameOpenedFolder(root, fi) {
		return false
	}
	names, records := windowsStaleEntries(root, ".", worktreePath)
	if windowsStaleEntryGoes(root, ".", names, records) && !wipesHomeDir(filepath.Join(base, names[0])) {
		_ = removeHeldEntry(root, names[0])
	}
	return len(names) > 0
}
