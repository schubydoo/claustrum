package main

import (
	"errors"
	"os"
	"path/filepath"
)

// The git directory of one git.worktree_create, held from its gate to its
// reply. It runs where gitDirMustBeReal is set (Windows).
//
// The rows are cells X1c and X1p of val-s3p2b2-7-windows (Windows VM). While a
// create runs, the driver renames `repo\.git` and puts a junction to another
// folder T at that name. 5fd08069 then refuses the create at its next step. It
// removes nothing in T and makes nothing in T, also where T is a copy of the
// git directory with an old `.claude-new-worktree-x` entry. The texts of the
// steps are at their places in gitWorktreeCreateLocked and worktreeAddStaged.
//
// How 5fd08069 tests the folder is not measured. claustrum holds the folder
// open and compares identities:
//
//   - The folder is read once after the trust check and before the gate
//     (gitDirIdentity). It must be a real directory: no link and no junction.
//   - After the gate the create opens it and holds it open until the reply
//     (acceptGitDir). The open folder must have that identity. While it is
//     open, no new folder gets that identity.
//   - Each later step tests that the path still names that folder (stands).
//   - The cleanup of old staging entries and the move of the new registration
//     act through the held folder (openAddStage), never through the path.
//
// A folder at the path that is another real directory is not measured. It gets
// the text of the step, as a link does.

// acceptedGitDir is the held git directory of one create.
type acceptedGitDir struct {
	path string
	root *os.Root
	info os.FileInfo
	// moved is true after a good `worktree add`: the repository then has the
	// registration of the new worktree.
	moved bool
}

// createStepHook is a test seam. It runs at the start of each test of the held
// git directory, with the name of the step. Production leaves it nil.
var createStepHook func(step string)

// realDirTail ends the text of a git directory that is no real directory. The
// name before it is quoted: 5fd08069 writes `".git" is a link or a file where
// Claude needs a real directory; put the directory itself in its place` for
// `C:\o\w\repo\.git` (cells X1c-06, X1c-10 and X1c-11). A git directory with
// another name is not measured: it gets its last name.
const realDirTail = " is a link or a file where Claude needs a real directory; put the directory itself in its place"

// The texts of three steps of a create on 5fd08069 (Windows VM): the step
// after `rev-parse --absolute-git-dir` (cells X1c-06 and X1p-06), the step
// before the `worktree add` call (cells X1c-09 and X1p-09, and table W3), and
// the step after that call (cells X1c-10 and X1p-10).
const (
	listEntriesPrefix      = "cannot list the repository's worktree entries: "
	prepareWorktreesPrefix = "cannot prepare the repository's worktrees directory: "
	completingAddPrefix    = "git worktree add failed (completing the new worktree's git files): "
)

// realDirText is the text of the git directory gitDir that is no real
// directory.
func realDirText(gitDir string) string {
	return quoteOperand(filepath.Base(gitDir)) + realDirTail
}

// gitDirIdentity is the Lstat of the git directory at path with its file
// identity read now, or nil when path is no real directory. On Windows
// os.SameFile reads the identity of a FileInfo from the path at the first
// compare (probeStat).
func gitDirIdentity(path string) os.FileInfo {
	fi, err := os.Lstat(path)
	if err != nil || !goesAsTree(fi.Mode()) {
		return nil
	}
	os.SameFile(fi, fi)
	return fi
}

// gitDirLinkDetail refuses a git directory that is a link or a junction, where
// gitDirMustBeReal is set. The text names the config of that directory. With a
// junction at `repo\.git`, 5fd08069 answers `this repository's git config
// cannot be used: "C:\\o\\w\\repo\\.git\\config": is, or is reached through, a
// link that Claude does not follow here; replace the link with what it points
// to` (cells X1c-01 to X1c-05, Windows VM). A link in a folder above the git
// directory is not tested: a repository below a junction answers normally
// (cell J1 of that round).
func gitDirLinkDetail(gitDir string) string {
	if !gitDirMustBeReal {
		return ""
	}
	if fi, err := os.Lstat(gitDir); err == nil && fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return gitDirLinkText(gitDir)
	}
	return ""
}

// gitDirLinkText is the detail of gitDirLinkDetail for gitDir.
func gitDirLinkText(gitDir string) string {
	return quoteOperand(filepath.Join(gitDir, "config")) + configLinkTail
}

// acceptGitDir opens the git directory at path and holds it. before is what
// gitDirIdentity gave for path before the gate of the request. It returns nil
// when path is no longer that real directory.
func acceptGitDir(path string, before os.FileInfo) *acceptedGitDir {
	if before == nil {
		return nil
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil
	}
	a := &acceptedGitDir{path: path, root: root, info: before}
	// The identity of the open folder comes from its handle.
	held, err := root.Open(".")
	if err == nil {
		var fi os.FileInfo
		fi, err = held.Stat()
		_ = held.Close()
		if err == nil && !os.SameFile(fi, before) {
			err = errors.New("another folder")
		}
	}
	if err != nil || !a.stands() {
		a.release()
		return nil
	}
	return a
}

// release closes the held folder. It is safe on nil.
func (a *acceptedGitDir) release() {
	if a != nil {
		_ = a.root.Close()
	}
}

// stands reports whether the path still names the held folder, as a real
// directory.
func (a *acceptedGitDir) stands() bool {
	fi := gitDirIdentity(a.path)
	return fi != nil && os.SameFile(fi, a.info)
}

// swapped runs the seam of step and reports whether the request holds a git
// directory that the path no longer names. It is false for a request that
// holds none.
func (u gitUse) swapped(step string) bool {
	if u.accepted == nil {
		return false
	}
	if createStepHook != nil {
		createStepHook(step)
	}
	return !u.accepted.stands()
}

// swapRefusal is the refusal of the gate of the request at a later step of a
// create in dir, or "". It runs the gate again when the path of the git
// directory no longer names the held folder: the call about the repository
// (askGit), then the read of the config (repoConfigGate).
//
// With a junction to a copy of the git directory, 5fd08069 makes that one
// call, which passes, and then answers the refusal of the config (cells X1c-01
// to X1c-05, Windows VM). With a junction to a folder that is no git directory
// it makes no such call and goes on: its next calls carry GIT_DIR=NUL (cells
// X1p-02 to X1p-06). A bad answer of the call at such a step is not measured:
// the create goes on.
func (u gitUse) swapRefusal(step, dir string) string {
	if !u.swapped(step) {
		return ""
	}
	t, ok := u.names(dir)
	if !ok {
		return ""
	}
	if u.host != nil && u.host.asksGit(t, false) && u.host.askGit(t, dir).bad() {
		return ""
	}
	msg, _ := repoConfigGate(t, false)
	return msg
}
