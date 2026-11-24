package git

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// The git directory of one git.worktree_create, held from its gate to its
// reply. It runs where gitDirMustBeReal is set: on every system.
//
// On Linux and macOS a symbolic link takes the place of the junction. For a
// link that is put there after `rev-parse --absolute-git-dir`, 5fd08069
// refuses there too and leaves T as it is (measured on Linux and macOS, see
// plainAddRefusal).
//
// The rows are measured on
// Windows. While a create runs, the driver renames `repo\.git` and puts a
// junction to another folder T at that name. 5fd08069 then refuses the create
// at its next step. It removes nothing in T and makes nothing in T, also where
// T is a copy of the git directory with an old `.claude-new-worktree-x` entry.
// The texts of the steps are at their places in gitWorktreeCreateLocked and
// worktreeAddStaged.
//
// How 5fd08069 tests the folder is not measured. claustrum holds the folder
// open and compares identities:
//
//   - The folder is read once after the trust check and before the gate
//     (gitDirIdentity). It must be a folder: no link and no junction
//     (heldFolderKind).
//   - After the gate the create opens it and holds it open until the reply
//     (acceptGitDir). The open folder must have that identity. While it is
//     open, no new folder gets that identity.
//   - Each later step tests that the path still names that folder (stands). A
//     step that fails the test refuses the create with the text of that step,
//     leaves the refusal to a later step (swapRefusal, answeredElsewhere), or
//     moves the hold (movedTo, askedAgain, replacedAtPath).
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
	// staged is the registration that the staging step moved into
	// `worktrees` (addStage.move), or the zero value.
	staged stagedEntry
	// reread is true for a hold that askedAgain took: the add reads its config
	// again after the listing (rereadConfigRefusal).
	reread bool
	// gitDirCopy is the folder in the folder of the daemon of a hold that
	// askedAgain took, or nil. The calls with --git-dir= name it
	// (takeGitDirCopy).
	gitDirCopy *gitUse
	// addBranch is the branch that the staged add made (`-b`), and addTip its
	// tip in the held folder right after the add, or "" (addStage.finish).
	addBranch, addTip string
	// junctionTo is the git directory of a second repository, when a junction in
	// baseRepo moved there right after `rev-parse --absolute-git-dir`
	// (noteJunctionMove). It is "" for every other create.
	junctionTo string
}

// stagedEntry is the name of a registration that the staging step moved into
// the folder `worktrees` of the held git directory, and its identity from the
// handle that the move held open from its mkdir to its return.
type stagedEntry struct {
	name string
	info os.FileInfo
}

// createStepHook is a test seam. It runs at the start of each test of the held
// git directory, with the name of the step. Production leaves it nil.
var createStepHook func(step string)

// realDirTail ends the text of a git directory that is no real directory. The
// name before it is quoted: 5fd08069 writes `".git" is a link or a file where
// Claude needs a real directory; put the directory itself in its place` for
// `C:\o\w\repo\.git` (measured). A git directory with another name is not
// measured: it gets its last name.
const realDirTail = " is a link or a file where Claude needs a real directory; put the directory itself in its place"

// The texts of three steps of a create on 5fd08069 (measured on Windows): the
// step after `rev-parse --absolute-git-dir`, the step before the `worktree
// add` call (measured), and the step after that call.
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
// identity read now, or nil when path is no folder (heldFolderKind). On
// Windows os.SameFile reads the identity of a FileInfo from the path at the
// first compare (probeStat).
//
// On Linux and macOS the mode and the identity come from the one Lstat call.
// On Windows they come from two opens of the path: Go has no call that gives
// both for a path from one handle and follows no link. From the code of Go
// 1.26.9 (os/types_windows.go, loadFileId), the second open is that of the
// entry itself, with FILE_FLAG_OPEN_REPARSE_POINT, and follows no junction. So
// the identity is that of the entry at the path at the second open. An entry
// that is a junction to the held folder has its own identity, not that of the
// folder. A path that changes between the two opens can therefore pass stands
// only when the entry at the second open is the held folder itself. That is
// sound for what the test decides: no act of the daemon inside the git
// directory goes through the path (gitdirheld.go). On Windows the test sees
// the junction at points 1 to 11 of a create (measured), and after the
// read-tree call at point 12 (measured).
func gitDirIdentity(path string) os.FileInfo {
	fi, err := os.Lstat(path)
	if err != nil || !heldFolderKind(fi.Mode()) {
		return nil
	}
	os.SameFile(fi, fi)
	return fi
}

// heldFolderKind reports whether an entry with the mode m of its Lstat can be
// the held git directory: a folder. With the default winsymlink setting, Go on
// Windows gives the folder bit only to a folder whose reparse tag is no name
// surrogate. So a junction, a directory symbolic link and every other name
// surrogate are no folder here. With winsymlink=0 Go gives the folder bit to a
// name surrogate that is no junction and no symbolic link. The open of the
// hold then refuses it (openHeldGitDir, from the code). No test covers such a
// name surrogate, as before heldFolderKind (claustrum's own, not measured). A
// `.git` folder with IO_REPARSE_TAG_PROJFS has the folder bit and the
// irregular bit. 4c111e86 makes the worktree of a create in shadow there
// (measured on Windows). Every other tag that is no name surrogate is a folder
// here too (claustrum's own, not measured). On Linux and macOS Go sets no
// irregular bit on a folder, so this is the test of goesAsTree there (from
// the code). goesAsTree still decides the kinds of the deletes.
func heldFolderKind(m fs.FileMode) bool {
	return m.IsDir()
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
// to` (measured on Windows). A link in a folder above the git directory is not
// tested: a repository below a junction answers normally (measured). A `.git`
// folder with the reparse tag IO_REPARSE_TAG_PROJFS is no link (folderIsLink).
// 4c111e86 answers git.info, git.status and git.list_branches there (measured
// on Windows). Every other tag that is no name surrogate is no link here too
// (claustrum's own, not measured).
func gitDirLinkDetail(gitDir string) string {
	if !gitDirMustBeReal {
		return ""
	}
	if fi, err := os.Lstat(gitDir); err == nil && folderIsLink(fi.Mode()) {
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
// when path is no longer that folder (heldFolderKind).
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

// answersOther reports whether gitDir, the answer of git to `rev-parse
// --absolute-git-dir`, names another folder than the git directory of the hold
// while the path still names the held folder. The git directory of the trust
// verdict counts, and so does the entry of a linked worktree. It is false on nil.
func (a *acceptedGitDir) answersOther(gitDir string) bool {
	if a == nil || a.isAt(gitDir) || !a.stands() {
		return false
	}
	for _, d := range []string{a.trust.pinCommonDir, a.trust.worktreeEntry} {
		if d != "" && (filepath.Clean(d) == gitDir || sameProbeFolder(gitDir, d)) {
			return false
		}
	}
	return true
}

// foundGitDir is the git directory of the hold in the "disagree" text: the
// entry of a linked worktree, or else the git directory of the trust verdict.
func (a *acceptedGitDir) foundGitDir() string {
	if a.trust.worktreeEntry != "" {
		return a.trust.worktreeEntry
	}
	return a.trust.pinCommonDir
}

// release closes the held folder. It is safe on nil.
func (a *acceptedGitDir) release() {
	if a != nil {
		_ = a.root.Close()
	}
}

// stands reports whether the path still names the held folder, as a folder
// (heldFolderKind).
//
// The spelling of the request is tested too (sent). On Linux and macOS a
// `.git` that is a symbolic link before the request is resolved by the trust
// check, so path is its target. A link that then leads to another folder is
// seen through sent. With such a change after `rev-parse --absolute-git-dir`,
// 5fd08069 fails the create and leaves no branch, registration or folder of
// the new worktree (measured on Linux and macOS).
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

// linksToHeld reports whether the path of the held git directory is now a
// symbolic link whose target is the held folder itself, also through the
// spelling of the request (sent). It is false on nil. The branch step of a
// rollback runs through such a link (runBranchStep).
func (a *acceptedGitDir) linksToHeld() bool {
	if a == nil {
		return false
	}
	for _, p := range []string{a.path, a.sent} {
		if p == "" {
			continue
		}
		now, err := os.Stat(p)
		if err != nil || !os.SameFile(now, a.info) {
			return false
		}
	}
	fi, err := os.Lstat(a.path)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// answeredElsewhere reports the two states in which a create on Windows gets
// no refusal right after `rev-parse --absolute-git-dir`, although the path no
// longer names the held folder. gitDir is the directory that git answered.
// The rows are of 5fd08069 on Windows (2 or 3 runs each). claustrum gives the
// same reply texts in them (measured on Windows, 2 runs each):
//
//   - Git answered another directory than the held one: a junction to a copy T
//     of the git directory, or a directory symbolic link to the git directory
//     X of a second repository, was put at `.git` before the call. 5fd08069
//     goes on and refuses at the step of the add with `git worktree add
//     failed: this repository's git config cannot be used: …` (measured
//     at point 5, also with the symbolic link). With the
//     junction it makes three calls on T first: one read of the config of T, a
//     listing with `--git-dir=<T>` and `rev-parse --show-toplevel` in T.
//     claustrum makes none of them (measured).
//   - The path is a regular file now: a file `gitdir: <X>` was put at `.git`
//     after the call. 5fd08069 goes on and refuses before the `worktree add`
//     with the text of openAddStage (measured with the file at point 6). Its
//     listing of the add carries GIT_DIR=<X> and a copy of X there. The
//     listing of claustrum keeps the variables of the request.
//
// A link that was put there after the call keeps the refusal of this step
// (measured at point 6 for both forms, also with the symbolic link).
// Another real directory at the path is not measured: it keeps that refusal
// too.
func answeredElsewhere(u gitUse, gitDir string) bool {
	if !u.accepted.isAt(gitDir) {
		return true
	}
	fi, err := os.Lstat(u.gitDir)
	return err == nil && fi.Mode().IsRegular()
}

// noteJunctionMove records a junction in baseRepo that now leads to the main git
// directory of a second repository, on Windows, right after `rev-parse
// --absolute-git-dir`. 4c111e86 and 87375cc7 then run the `worktree add` with
// GIT_DIR=<second repository>, so the branch lands there. The registration and the
// `.git` file of the leaf land in the held folder, and the leaf stays where it was
// made. The create answers unsafe_path with no rollback (measured on Windows, by the
// maintainer's decision of 2026-11-23). It reports false for every other state: a
// held folder that is gone, a linked worktree on either side, or no junction. Those
// states keep the refusals of the steps. It runs at this one step, so a junction
// moved at another step keeps its refusal too.
func (a *acceptedGitDir) noteJunctionMove(repo string) bool {
	to := a.junctionLeadsTo(repo)
	if to == "" {
		return false
	}
	a.junctionTo = to
	return true
}

// behindMovedJunction reports that a junction in baseRepo leads elsewhere now, on
// Windows, while the held folder stands at its own path. The new target can be a
// folder with no git directory (87375cc7, measured on Windows).
func (a *acceptedGitDir) behindMovedJunction(repo string) bool {
	if a == nil || !stagedAddWindowsSteps || a.trust.worktreeEntry != "" || a.stands() {
		return false
	}
	if _, through := junctionFreePath(repo); !through {
		return false
	}
	now := gitDirIdentity(a.path)
	return now != nil && os.SameFile(now, a.info)
}

// junctionLeadsTo is the main git directory of a second repository that a junction
// in baseRepo leads to now, on Windows, while the held folder stands at its own path.
// It is "" in every other state.
func (a *acceptedGitDir) junctionLeadsTo(repo string) string {
	if a == nil || !stagedAddWindowsSteps || a.trust.worktreeEntry != "" {
		return ""
	}
	if _, through := junctionFreePath(repo); !through {
		return ""
	}
	if now := gitDirIdentity(a.path); now == nil || !os.SameFile(now, a.info) {
		return ""
	}
	t := gitDirTrustFor(repo)
	if t.verdict != gitDirTrusted || t.pinCommonDir == "" || t.worktreeEntry != "" || a.isAt(t.pinCommonDir) {
		return ""
	}
	return t.pinCommonDir
}

// junctionMoved reports that noteJunctionMove recorded a move. It is safe on nil.
func (a *acceptedGitDir) junctionMoved() bool {
	return a != nil && a.junctionTo != ""
}

// movedTo is the hold of a create on the git directory gitDir that git
// answered, when that is another folder (heldFolderKind) than the held one and
// passes the gate. It is nil in every other case. repo is the baseRepo of the create.
// The caller releases the new hold.
//
// The rows are measured on Linux and macOS (2 of 2 runs on macOS). A symbolic link to a
// copy T of the git directory is put at `.git` before `rev-parse
// --absolute-git-dir`. Git then answers T. 5fd08069 reads the config again,
// goes on in T and answers success: T has the registration, the branch and the
// index of the new worktree, and the `.git` file of the leaf names
// `T/worktrees/<name>`. Its calls in baseRepo keep GIT_DIR=<baseRepo>/.git,
// and its calls with --git-dir= name T.
//
// The new hold keeps the trust verdict of the gate, so the calls in baseRepo
// keep their variables. Not measured: a T that is the git directory of another
// repository, and a second change after this step.
func (a *acceptedGitDir) movedTo(gitDir, repo string) *acceptedGitDir {
	if a == nil || a.isAt(gitDir) {
		return nil
	}
	return a.takeOver(gitDir, gitDirIdentity(gitDir), repo)
}

// mayAskAgain reports whether a create can ask git again at the add
// (askedAgain), right after a first answer gitDir of `rev-parse
// --absolute-git-dir` while the path no longer names the held folder. The
// answer must name the held folder, and the `commondir` test of the step must
// pass. The trust check of repo must pin another git directory. It is false on
// nil.
func (a *acceptedGitDir) mayAskAgain(gitDir, repo string) bool {
	if a == nil || !a.isAt(gitDir) {
		return false
	}
	if _, err := os.Lstat(filepath.Join(gitDir, "commondir")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	t := gitDirTrustFor(repo)
	return t.verdict == gitDirTrusted && t.pinCommonDir != "" && !a.isAt(t.pinCommonDir)
}

// askedAgain is the hold of a create on the git directory that git answers in
// a second `rev-parse --absolute-git-dir` call, with that answer. The create
// makes the call at the add, when mayAskAgain was true and the path changed
// before the listing of the add. It is nil when the call fails or movedTo
// gives no hold. The caller releases the new hold.
//
// The rows are measured on Linux. A symbolic link to a copy of the git
// directory is put at `.git` right after the first answer. 4c111e86 then makes
// the call about the repository, the read of the config and the listing of the
// add. Next it asks git again, goes on in the copy and answers success. The
// record of the session folder names the copy. In the copy the listing comes
// first, then a new read of its config (reread), the pair and the add. A link
// to the git directory of another repository is tested, not measured. macOS
// follows Linux (not measured).
func (a *acceptedGitDir) askedAgain(u gitUse, repo string) (string, *acceptedGitDir) {
	again, _, answered, err := worktreeBaseGitDir(u, repo)
	if a == nil || err != nil || !answered {
		return "", nil
	}
	next := a.movedTo(again, repo)
	if next == nil {
		return "", nil
	}
	next.reread = true
	return again, next
}

// takeGitDirCopy makes the folder of the git directory pinned in the folder of
// the daemon, with read, the read of its config. pinned and read come from one
// walk and one read (rereadConfigRefusal), so no step resolves the path again.
// It is the step of the first gate of a request (syncRepo). That step writes
// and evicts only inside the folder of the daemon: the 129th repository folder
// evicts the oldest one.
//
// a is the hold that askedAgain took. After that answer 4c111e86 names one new
// folder of the copy in GIT_COMMON_DIR for each call with --git-dir=<copy>.
// The other calls that name a folder of a repository keep the folder of the
// first gate (measured on Linux). A pinned folder that is not the held folder
// gets no copy, and so does a step that fails. The calls then keep the folder
// of the first gate (claustrum's own, not measured).
func (a *acceptedGitDir) takeGitDirCopy(u gitUse, pinned string, read *repoVerdict) {
	if read == nil || u.host == nil || pinned == "" {
		return
	}
	if now := gitDirIdentity(pinned); now == nil || !os.SameFile(now, a.info) {
		return
	}
	if use, err := u.host.syncRepo(pinned, read); err == nil {
		a.gitDirCopy = &use
	}
}

// replacedAtPath is the hold of a create on a new folder (heldFolderKind) at
// the path of the held git directory, right after a good `worktree add`. It is
// nil when the path names no such folder, still names the held folder, or
// names a folder that fails the gate. repo is the baseRepo of the create. The caller
// releases the new hold.
//
// The row is measured on Linux and macOS. While
// `worktree add` runs, `.git` is renamed and a real copy of it is put at the
// path. Git makes the registration in the copy, because the link `worktrees`
// of the folder of the daemon names the path. 5fd08069 answers success: the
// copy has the registration with its index and the branch, the worktree is
// checked out, and the old folder is unchanged. The maintainer decided on
// 2026-10-14 to follow that outcome. The steps after the add then act through
// the copy.
//
// After the move a failed read-tree rolls back in the copy, and the old folder
// is unchanged (measured on Linux). A copy made before the add holds no
// registration, and the tests of the registration refuse it with unsafe_path
// (measured). Not measured: a swap at another moment, and a folder of another
// repository. A link at the path keeps the refusal of the rows of
// TestCreateWithAGitDirThatChangedAtTheAdd.
func (a *acceptedGitDir) replacedAtPath(repo string) *acceptedGitDir {
	if a == nil {
		return nil
	}
	now := gitDirIdentity(a.path)
	if now == nil || os.SameFile(now, a.info) {
		return nil
	}
	next := a.takeOver(a.path, now, repo)
	if next != nil {
		next.moved = true
	}
	return next
}

// adopt makes a the hold that next holds, and closes the folder that a held,
// so that every user of a acts through the new folder. The staging step of a
// create calls it after replacedAtPath. next is not released on its own.
func (a *acceptedGitDir) adopt(next *acceptedGitDir) {
	if a.root != nil {
		_ = a.root.Close()
	}
	branch, tip := a.addBranch, a.addTip
	*a = *next
	a.addBranch, a.addTip = branch, tip
}

// takeOver is the hold on the folder (heldFolderKind) gitDir, whose identity
// before the open is before, when the trust check of repo pins it and its
// config passes the gate. The new hold keeps the trust verdict of the first
// gate.
func (a *acceptedGitDir) takeOver(gitDir string, before os.FileInfo, repo string) *acceptedGitDir {
	t := gitDirTrustFor(repo)
	if before == nil || t.verdict != gitDirTrusted || !sameGitDirPath(t.pinCommonDir, gitDir) {
		return nil
	}
	if msg, _ := repoConfigGate(t, false, nil); msg != "" {
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
// call, which passes, and then answers the refusal of the config (measured on
// Windows. One measured case makes more calls first). With a junction to a
// folder that is no git directory it makes no such call and goes on: its next
// calls carry GIT_DIR=NUL (measured). A bad answer of the call at such a step
// is not measured: the create goes on.
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
//     refuses before the `worktree add` (measured with the file
//     at point 6, Windows, 2 of 2 runs). That refusal is in openAddStage. Each other
//     step refuses with the text of a git directory behind a link, and no call runs in X.
//     The text is the one of the junction rows. For a file, 5fd08069 has another text at
//     the `worktree add` (measured on Windows, 2 runs each). At the
//     first listing of the checkout claustrum now runs the second listing for a file, as
//     5fd08069 does (gitUse.listedSwap). At the second listing and at the read-tree call
//     the texts are equal. The step `branch` refuses so too, so no branch call runs with a
//     `.git` file that names another repository.
//   - On Linux and macOS the gate runs on that git directory, and a gate that
//     passes gives no refusal here. With a symbolic link at `.git` to a copy
//     of the git directory, put there after `rev-parse --absolute-git-dir`,
//     5fd08069 makes the call about the repository and the read of the config,
//     then the listing of the add, and refuses at the step before the add
//     (measured on macOS, 7 of 7 runs). The refusal
//     is in worktreeAdd. With that link before the call, git answers the copy,
//     and the create goes on in it (movedTo). With that link right after the
//     call, 4c111e86 goes on in the copy too (askedAgain, measured on Linux).
func (u gitUse) swapRefusal(step, dir string) string {
	if !u.swapped(step) {
		return ""
	}
	t := gitDirTrustFor(dir)
	if !u.shadow() || daemonCommonDirStays() || t.verdict != gitDirTrusted || t.pinCommonDir == "" {
		return ""
	}
	if stagedAddWindowsSteps && step != "add" && !sameGitDirPath(t.pinCommonDir, u.gitDir) {
		return repoConfigPrefix + gitDirLinkText(u.gitDir)
	}
	if u.host != nil && u.host.asksGit(t, false) && u.host.askGit(t, dir).bad() {
		return ""
	}
	msg, _ := repoConfigGate(t, false, u.stop)
	return msg
}
