package main

import (
	"errors"
	"os"
	"path/filepath"
)

// The git directory of one git.worktree_create, held from its gate to its
// reply. It runs where gitDirMustBeReal is set: on every system.
//
// On Linux and macOS VMs a symbolic link takes the place of the junction. For
// a link that is put there after `rev-parse --absolute-git-dir`, 5fd08069
// refuses there too and leaves T as it is (the swap cells of
// val-s3p2b2-9-linux2 and val-s3p2b2-9-macos, see plainAddRefusal).
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
//   - Each later step tests that the path still names that folder (stands). A
//     step that fails the test refuses the create with the text of that step,
//     leaves the refusal to a later step (swapRefusal, answeredElsewhere), or
//     moves the hold (movedTo).
//   - The acts of the create inside the git directory go through the held
//     folder, never through the path. The list is at the top of gitdirheld.go.
//     The test of a step is therefore no guard of such an act. It decides the
//     reply, and whether a git call runs: git takes the git directory as a
//     path.
//
// A folder at the path that is another real directory is not measured. Its
// trust check pins the same path. At the steps `leaf`, `gitdir` and `add` it
// gets no refusal when its config is accepted (swapRefusal). On Windows the
// step `answered` is the first that refuses it, with the text of that step. On
// Linux and macOS it is the step before the add (plainAddRefusal).

// acceptedGitDir is the held git directory of one create.
type acceptedGitDir struct {
	path string
	root *os.Root
	info os.FileInfo
	// dir is the baseRepo of the create, and trust is its trust verdict at the
	// gate. The calls of the create in dir keep naming the copy of that
	// verdict (gitUse.names).
	dir   string
	trust gitDirTrust
	// sent is `<dir>/.git` as the request spells it, when that path named the
	// held folder at the hold (holdFor). It is "" otherwise.
	sent string
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
//
// On Linux and macOS the mode and the identity come from the one Lstat call.
// On Windows they come from two opens of the path: Go has no call that gives
// both for a path from one handle and follows no link. From the code of Go
// 1.26.9 (os/types_windows.go, loadFileId), the second open is that of the
// entry itself, with FILE_FLAG_OPEN_REPARSE_POINT, and follows no junction. So
// the identity is that of the entry at the path at the second open. An entry
// that is a junction to the held folder has its own identity, not that of the
// folder. A path that changes between the two opens can therefore pass
// stands only when the entry at the second open is the held folder itself.
// That is sound for what the test decides: no act of the daemon inside the
// git directory goes through the path (gitdirheld.go). On a Windows VM the
// test sees the junction at points 1 to 11 of a create (the table of section
// 2 of val-s3p2b2-12-windows), and after the read-tree call at point 12
// (section 1.4 of val-s3p2b2-13-windows).
func gitDirIdentity(path string) os.FileInfo {
	fi, err := os.Lstat(path)
	if err != nil || !goesAsTree(fi.Mode()) {
		return nil
	}
	os.SameFile(fi, fi)
	return fi
}

// gitDirLinkDetail refuses a git directory that is a junction, where
// gitDirMustBeReal is set. On Linux and macOS a symbolic link at `.git` that
// is there before the request is resolved by the trust check first, so gitDir
// is then its target. On Windows the trust check keeps a directory symbolic
// link as it is spelled, and it is refused here (gitDirIsSymlink). The text
// names the config of that directory. With a
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
	root, err := openHeldGitDir(path)
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

// holdFor records the baseRepo dir of the create and its trust verdict t. It
// also decides, once and by identity, whether `<dir>/.git` is the held folder:
// a baseRepo can be spelled in another way than the resolved path of its git
// directory (a symbolic link above it, a short name on Windows). The decision
// is made here, while the hold is fresh, and not at a later step: a folder
// that is put at the path afterwards has another identity, and a step that
// asked then went to that folder by its path.
func (a *acceptedGitDir) holdFor(dir string, t gitDirTrust) {
	a.dir, a.trust, a.sent = dir, t, ""
	sent := filepath.Join(dir, ".git")
	if fi, err := os.Stat(sent); err == nil && os.SameFile(fi, a.info) {
		a.sent = sent
	}
}

// isAt reports whether gitDir is a path of the held folder: its resolved path
// by text, or the spelling that holdFor accepted. It is safe on nil.
func (a *acceptedGitDir) isAt(gitDir string) bool {
	if a == nil {
		return false
	}
	g := filepath.Clean(gitDir)
	return sameGitDirPath(g, filepath.Clean(a.path)) || (a.sent != "" && g == a.sent)
}

// release closes the held folder. It is safe on nil.
func (a *acceptedGitDir) release() {
	if a != nil {
		_ = a.root.Close()
	}
}

// stands reports whether the path still names the held folder, as a real
// directory.
//
// The spelling of the request is tested too (sent). On Linux and macOS a
// `.git` that is a symbolic link before the request is resolved by the trust
// check, so path is its target. A link that then leads to another folder is
// seen through sent.
// With such a change after `rev-parse --absolute-git-dir`, 5fd08069 fails the
// create and leaves no branch, registration or folder of the new worktree
// (cell dlink-swap of val-s3p2b2-10-linux2 and cell d-dswap of
// val-s3p2b2-10-macos, Linux and macOS VMs).
func (a *acceptedGitDir) stands() bool {
	fi := gitDirIdentity(a.path)
	if fi == nil || !os.SameFile(fi, a.info) {
		return false
	}
	if a.sent == "" {
		return true
	}
	sent, err := os.Stat(a.sent)
	return err == nil && os.SameFile(sent, a.info)
}

// answeredElsewhere reports the two states in which a create on Windows gets
// no refusal right after `rev-parse --absolute-git-dir`, although the path no
// longer names the held folder. gitDir is the directory that git answered.
// The rows are of 5fd08069 on a Windows VM (val-s3p2b2-12-windows, 2 or 3 runs
// each). claustrum gives the same reply texts in them (table X1 and part C of
// val-s3p2b2-13-windows, Windows VM, 2 runs each):
//
//   - Git answered another directory than the held one: a junction to a copy T
//     of the git directory, or a directory symbolic link to the git directory
//     X of a second repository, was put at `.git` before the call. 5fd08069
//     goes on and refuses at the step of the add with `git worktree add
//     failed: this repository's git config cannot be used: …` (table of
//     section 2, point 5, and part C, the symbolic link at point 5). With the
//     junction it makes three calls on T first: one read of the config of T, a
//     listing with `--git-dir=<T>` and `rev-parse --show-toplevel` in T.
//     claustrum makes none of them (section 1.7 of val-s3p2b2-13-windows).
//   - The path is a regular file now: a file `gitdir: <X>` was put at `.git`
//     after the call. 5fd08069 goes on and refuses before the `worktree add`
//     with the text of openAddStage (part C, the file at point 6). Its
//     listing of the add carries GIT_DIR=<X> and a copy of X there. The
//     listing of claustrum keeps the variables of the request.
//
// A link that was put there after the call keeps the refusal of this step
// (point 6 of both forms, and the symbolic link at point 6 of part C).
// Another real directory at the path is not measured: it keeps that refusal
// too.
func answeredElsewhere(u gitUse, gitDir string) bool {
	if !u.accepted.isAt(gitDir) {
		return true
	}
	fi, err := os.Lstat(u.gitDir)
	return err == nil && fi.Mode().IsRegular()
}

// movedTo is the hold of a create on the git directory gitDir that git
// answered, when that is another real directory than the held one and passes
// the gate. It is nil in every other case. repo is the baseRepo of the create.
// The caller releases the new hold.
//
// The rows are cells swb-first and swb-abbrev of val-s3p2b2-10-linux2 and
// cells b-first and b-abbrev of val-s3p2b2-10-macos (Linux and macOS VMs, 2 of
// 2 runs on macOS). A symbolic link to a copy T of the git directory is put at
// `.git` before `rev-parse --absolute-git-dir`. Git then answers T. 5fd08069
// reads the config again, goes on in T and answers success: T has the
// registration, the branch and the index of the new worktree, and the `.git`
// file of the leaf names `T/worktrees/<name>`. Its calls in baseRepo keep
// GIT_DIR=<baseRepo>/.git, and its calls with --git-dir= name T.
//
// The new hold keeps the trust verdict of the gate, so the calls in baseRepo
// keep their variables. Not measured: a T that is the git directory of another
// repository, and a second change after this step.
func (a *acceptedGitDir) movedTo(gitDir, repo string) *acceptedGitDir {
	if a == nil || a.isAt(gitDir) {
		return nil
	}
	before := gitDirIdentity(gitDir)
	t := gitDirTrustFor(repo)
	if before == nil || t.verdict != gitDirTrusted || !sameGitDirPath(t.pinCommonDir, gitDir) {
		return nil
	}
	if msg, _ := repoConfigGate(t, false); msg != "" {
		return nil
	}
	next := acceptGitDir(gitDir, before)
	if next != nil {
		next.holdFor(repo, a.trust)
	}
	return next
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
// to X1c-04, Windows VM. Cell X1c-05 makes more calls first). With a junction
// to a folder that is no git directory
// it makes no such call and goes on: its next calls carry GIT_DIR=NUL (cells
// X1p-02 to X1p-06). A bad answer of the call at such a step is not measured:
// the create goes on.
//
// The trust check can also pin another git directory than the held one: a
// `.git` that became a file with `gitdir: <X>`, or on Linux and macOS a
// symbolic link to X, where X is a git directory that the trust check
// accepts. On Windows a directory symbolic link at `.git` is pinned as it is
// spelled, like a junction (gitDirIsSymlink).
//
//   - On Windows the step `add` then runs the gate on X, and a gate that
//     passes gives no refusal. With a file `gitdir: <X>` at `.git` after
//     `rev-parse --absolute-git-dir`, 5fd08069 makes the call about the
//     repository, one read of a config and the listing of the add, and then
//     refuses before the `worktree add` (part C of val-s3p2b2-12-windows, the
//     file at point 6, Windows VM, 2 of 2 runs). That refusal is in
//     openAddStage. Each other step refuses with the text of a git directory
//     behind a link, and no call runs in X. The text is the one of the
//     junction rows. For a file, 5fd08069 has another text at two of those
//     steps: at the `worktree add` and at the first listing of the checkout
//     (table C of val-s3p2b2-13-windows, Windows VM, 2 runs each). At the
//     second listing and at the read-tree call the texts are equal. The
//     step `branch` refuses so too, so no branch call runs with a `.git` file
//     that names another repository.
//   - On Linux and macOS the gate runs on that git directory, and a gate that
//     passes gives no refusal here. With a symbolic link at `.git` to a copy
//     of the git directory, put there after `rev-parse --absolute-git-dir`,
//     5fd08069 makes the call about the repository and the read of the config,
//     then the listing of the add, and refuses at the step before the add
//     (cells swap-copy of val-s3p2b2-9-macos, macOS VM, 7 of 7 runs). The
//     refusal is in worktreeAdd. With that link before the call, git answers
//     the copy, and the create goes on in it (movedTo).
func (u gitUse) swapRefusal(step, dir string) string {
	if !u.swapped(step) {
		return ""
	}
	t := gitDirTrustFor(dir)
	if !u.shadow() || daemonCommonDirStays() || t.verdict != gitDirTrusted || t.pinCommonDir == "" {
		return ""
	}
	if shadowStagesAdd && step != "add" && !sameGitDirPath(t.pinCommonDir, u.gitDir) {
		return repoConfigPrefix + gitDirLinkText(u.gitDir)
	}
	if u.host != nil && u.host.asksGit(t, false) && u.host.askGit(t, dir).bad() {
		return ""
	}
	msg, _ := repoConfigGate(t, false)
	return msg
}
