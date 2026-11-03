package git

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/schubydoo/claustrum/internal/homeguard"
	"github.com/schubydoo/claustrum/internal/pathcanon"
)

// worktreeCheckpoint records how the freshly created worktree leaf looked right
// after mkdirWorktreeLeaf, so verifyCreatedWorktree can confirm `git worktree add`
// populated that same directory rather than one swapped in underneath it.
type worktreeCheckpoint struct {
	info       os.FileInfo // identity (dev+inode, or volume serial+file index, via os.SameFile) of the created leaf
	parentInfo os.FileInfo // identity of the leaf's parent, read from the held parent handle
	resolved   string      // the leaf path with every symlink component resolved
	held       []*os.File  // open handles on the leaf and its parent, see holdWorktreeDir
}

// release closes the handles that the checkpoint holds. The create calls it once,
// when it answers.
func (cp worktreeCheckpoint) release() {
	for _, f := range cp.held {
		_ = f.Close()
	}
}

// evalSymlinks is filepath.EvalSymlinks behind a seam, so the failure arm of
// checkpointCreatedWorktree is reachable from a test. baseRepoWalkFails uses the seam
// too. Production never reassigns it.
var evalSymlinks = filepath.EvalSymlinks

// checkpointCreatedWorktree captures the leaf's identity for the post-add check.
// A capture failure yields an empty checkpoint, which verifyCreatedWorktree treats
// as "nothing to compare against" and passes — the verification is a best-effort
// guard, never a new way to fail an honest create.
//
// The checkpoint also holds the leaf and its parent open until the create answers
// (release). The identity comes from the held leaf handle. On unix, while the
// handle is open, the file system cannot give the leaf's inode number to a new
// directory. Without the hold, ext4 gave a deleted leaf's number to its replacement
// 4 times in 6. os.SameFile then took the replacement for the leaf. On Windows an
// identity from os.Stat of the path is read from the path only when os.SameFile
// runs, so it read the replacement too, and a replaced leaf passed the check. The
// references hold the leaf and its parent open during the create (measured in
// /proc/<pid>/fd on Linux and with handle64 on Windows). The caller must call
// release.
func checkpointCreatedWorktree(worktreePath string) worktreeCheckpoint {
	var cp worktreeCheckpoint
	var info os.FileInfo
	var err error
	if leaf := holdWorktreeDir(worktreePath); leaf != nil {
		cp.held = append(cp.held, leaf)
		if parent := holdWorktreeDir(filepath.Dir(filepath.Clean(worktreePath))); parent != nil {
			cp.held = append(cp.held, parent)
			if pi, err := parent.Stat(); err == nil {
				cp.parentInfo = pi
			}
		}
		info, err = leaf.Stat()
	} else {
		info, err = os.Stat(worktreePath)
	}
	if err != nil {
		cp.release()
		return worktreeCheckpoint{}
	}
	resolved, err := evalSymlinks(worktreePath)
	if err != nil {
		cp.release()
		return worktreeCheckpoint{}
	}
	cp.info, cp.resolved = info, resolved
	return cp
}

// undoFailedAdd rolls back a `git worktree add` that git reported as failed, when no
// attach fallback is left to try. It runs no git call. It removes the leaf only if the
// leaf is an empty directory. A registration or a branch that the failed add made
// stays, and so does any content in the leaf. Measured against f6010b97 and 90fca6e6
// on macOS and Windows. A retry at the same path therefore succeeds after the common
// failures (a branch that already exists, a ref lock), which leave the leaf empty and
// unregistered.
//
// The removal is an rmdir, so it can never delete a file or a directory with an
// entry in it. It still keeps the always-on home guard (D2) and the part of the leaf
// identity check that catches an ancestor swapped for a symlink: the path must still
// resolve to where the leaf was created. A leaf that the failed add replaced with a
// new empty directory at that same place is removed, as measured against both
// references.
//
// The leaf's parent must also still be the directory that the checkpoint holds. If
// the failed add replaced the parent and made a new empty leaf in it, the new leaf
// stays. f6010b97 and 90fca6e6 keep it after a failed attach add, in 20 of 20 runs
// on Linux and macOS.
func undoFailedAdd(worktreePath string, cp worktreeCheckpoint) {
	if worktreePath == "" || homeguard.WipesHomeDir(worktreePath) {
		return
	}
	if cp.info != nil {
		if resolved, err := filepath.EvalSymlinks(worktreePath); err != nil || resolved != cp.resolved {
			return
		}
	}
	if cp.parentInfo != nil {
		now, err := os.Stat(filepath.Dir(filepath.Clean(worktreePath)))
		if err != nil || !os.SameFile(cp.parentInfo, now) {
			return
		}
	}
	_ = rmdirWorktreeLeaf(worktreePath)
}

// undoFailedCheckout rolls back git.worktree_create after the add succeeded: after
// a failed read-tree checkout, a failed config.worktree step, a failed placement of
// the index (Linux and macOS),
// a failed spelling call of git for the registration (spellingRetryText, Linux and macOS),
// or after the caller's timeoutMs expired during the
// add, the checkout, a checkout drain that overran the drain cap, or the copy step.
// It returns "" when the undo finished, or the text that the caller appends to the
// frame's error. The errorCode of the frame does not change. kept is true when the
// branch step kept the branch because it holds commits that no other ref reaches.
// The frame then carries "branchKept":true. The steps and the leaf texts were
// measured against f6010b97 and 90fca6e6 on Windows:
//
//   - Step A deletes the entries at the top of the leaf, one at a time, in the order
//     that the directory read returns them, not sorted. It stops at the first entry
//     that it cannot delete. Then the text is
//     "; and the undo could not finish for <leaf>: the worktree directory, its
//     registration, and the branch all remain; remove them by hand before retrying
//     (RemoveAll <entry>: <OS error>)", and nothing else is undone. In attach mode
//     the call made no branch, and the text reads "the worktree directory and its
//     registration both remain" instead (`f6010b97`, `89cb6289`, measured).
//
//   - Step B deletes the worktree's registration, then runs the branch step
//     (runBranchStep) on the branch that the call created. In attach mode branch is
//     "", so no git call runs. After the attach fallback, the call created
//     branchName. The caller's expired deadline does not stop the branch step
//     (measured). If the registration cannot be deleted, the branch step does not
//     run, step C still runs, and the text is "; and the undo could not finish for
//     <leaf>: the worktree registration and the branch remain; remove them by hand
//     before retrying (RemoveAll <registration name>: <OS error>)". 89cb6289 gives
//     it after a failed placement of the index, with the leaf gone (measured on
//     Linux and macOS). In attach mode the text reads "the worktree registration
//     remains; remove it by hand before retrying (...)" instead (measured on Linux
//     and macOS). A step C that fails too adds no text of its own (measured on
//     Linux). A failed read-tree checkout and a timeout during the checkout give
//     the same clause (measured on macOS). The same texts after the two other
//     timeout frames are claustrum's choice (not measured). On Windows, for a
//     create with no moved registration, a registration that cannot be deleted adds
//     no text, and the branch step runs (removeCreatedRegistration): Windows is not
//     measured.
//
//     Which registration goes depends on tested. With tested set (Linux and macOS,
//     after the tests of createdRegistrationRefusal, and on Windows the moved
//     registration), it is the entry that those tests accepted
//     (removeTestedRegistration). With noRegistration set, none goes. Neither the .git file of the leaf
//     nor the gitdir record is read again. After a failed spelling call the
//     caller gives the one new entry of the registrations folder in its place. With tested not set, it is the entry that
//     the .git file of the leaf names now, and only if its record names the leaf
//     (createdWorktreeAdminDir). There a back-pointer that cannot be read for a
//     permission error counts as a registration that stays, and no delete is
//     attempted.
//
//   - Step C removes the leaf directory, which is now empty. If that fails, the text
//     is "; and the undo could not finish for <leaf>: the worktree directory remains
//     (re-populated while undoing?); remove it by hand before retrying (removeat
//     <leaf base name>: <OS error>)".
//
// A branch that the branch step keeps adds its own part (rollbackBranchText) after
// the leaf part, joined by "; " (measured). A kept branch together with a failed
// rmdir is the only pair that a row measured. Any other pair of the two parts uses
// the same join. That is claustrum's choice (not measured).
//
// With a plain baseRepo, the registrations directory (<common git dir>/worktrees)
// stays, empty when the call created it. With a linked-worktree baseRepo, the
// registration in the main repository's worktrees directory is removed and the
// linked worktree's own entry stays. No `git worktree remove` runs, because it
// deletes an empty registrations directory.
//
// Steps A and C are recursive or plain deletes of an RPC-supplied path, so each one
// first applies the always-on home guard (D2) and the leaf identity check. A refusal
// skips the leaf and adds no leaf text. The leaf then stays, so a kept branch gets
// its text without the "the worktree itself was removed, but " start. That is
// claustrum's own guard (not measured): no honest input reaches it.
func undoFailedCheckout(u gitUse, repo, worktreePath, branch string, cp worktreeCheckpoint, tested testedRegistration) (text string, kept bool) {
	// Without tested, the admin dir is found through the leaf's .git file, which
	// step A deletes.
	var registration createdRegistration
	// A create that holds its git directory deletes no registration when the
	// path no longer names that folder (acceptedGitDir). After a good add the
	// repository has the registration, and the rollback then answers that it
	// remains, with the text of a git directory that is no real directory. On
	// 5fd08069 the registration stays and the leaf goes (measured on Windows).
	// After an add that the daemon did not complete there is no registration to
	// delete, and the branch step runs (measured).
	//
	// With the path still on the held folder, the registration goes through
	// that folder. With tested set, removeTestedRegistration opens the
	// registrations folder through it. With tested not set,
	// acceptedGitDir.removeCreatedRegistration does. heldRegistration is the
	// path that the `.git` file of the leaf names now. A path that changes
	// after the tests leads the delete nowhere else.
	//
	// The path is tested two times: here, and again after step A. With a junction that
	// is put at `repo\.git` while step A clears a leaf of 5000 files, 5fd08069 keeps
	// the registration and the branch and answers that they remain (measured with both
	// forms of T, Windows, 2 of 2 runs), and so does claustrum (measured, 2 runs). With
	// a symbolic link, 5fd08069 and claustrum do the same (measured on Linux and macOS,
	// 7 of 7 runs).
	var heldErr error
	var heldRegistration string
	held := false
	testHeld := func(step string) {
		if held || !u.swapped(step) {
			return
		}
		held, tested = true, testedRegistration{}
		if u.accepted.moved {
			heldErr = errors.New(realDirText(u.accepted.path))
		}
	}
	testHeld("undo")
	switch {
	case tested.noRegistration:
	case held:
	case tested.path != "":
	case u.accepted != nil:
		if admin := worktreeAdminDir(worktreePath); admin != "" {
			heldRegistration = absoluteAdminDir(worktreePath, admin)
		}
	default:
		registration = createdWorktreeAdminDir(repo, worktreePath)
	}
	leafSafe := func() bool { return leafSafeToRemove(worktreePath, cp) }
	touchLeaf := leafSafe()
	if touchLeaf {
		if detail := clearWorktreeLeaf(worktreePath); detail != "" {
			remain := "the worktree directory, its registration, and the branch all remain"
			if branch == "" {
				remain = "the worktree directory and its registration both remain"
			}
			return fmt.Sprintf("; and the undo could not finish for %s: %s; remove them by hand before retrying (%s)", worktreePath, remain, detail), false
		}
	}
	testHeld("leaf cleared")
	// A registration that cannot be deleted stays, and then the branch step does not
	// run: 89cb6289 runs no git call after it (measured on Linux).
	var res branchStepResult
	var regErr error
	switch {
	case tested.noRegistration:
	case held:
		regErr = heldErr
	case tested.path != "":
		regErr = removeTestedRegistration(tested)
	case u.accepted != nil:
		u.accepted.removeCreatedRegistration(heldRegistration, worktreePath)
	default:
		regErr = removeCreatedRegistration(registration)
	}
	if regErr == nil {
		res = runBranchStep(u, repo, branch)
	}
	var parts []string
	leafGone := false
	if touchLeaf && leafSafe() {
		if err := removeEmptyLeaf(worktreePath); err != nil {
			parts = append(parts, fmt.Sprintf("the worktree directory remains (re-populated while undoing?); remove it by hand before retrying (%v)", err))
		} else {
			leafGone = true
		}
	}
	if regErr != nil {
		// The registration text stands alone, also when the leaf rmdir failed too
		// (measured on Linux). In attach mode the call made no branch (measured).
		remain := "the worktree registration and the branch remain; remove them"
		if branch == "" {
			remain = "the worktree registration remains; remove it"
		}
		return fmt.Sprintf("; and the undo could not finish for %s: %s by hand before retrying (%v)", worktreePath, remain, regErr), false
	}
	if t := rollbackBranchText(repo, branch, res, !leafGone); t != "" {
		parts = append(parts, t)
	}
	if len(parts) == 0 {
		return "", false
	}
	return "; and the undo could not finish for " + worktreePath + ": " + strings.Join(parts, "; "), res.kind == branchUnique
}

// leafSafeToRemove is the guard of each delete of the leaf in a rollback after a
// good add: the home guard (D2) and the checkpoint identity of the leaf.
func leafSafeToRemove(worktreePath string, cp worktreeCheckpoint) bool {
	return worktreePath != "" && !homeguard.WipesHomeDir(worktreePath) && verifyCreatedWorktree(worktreePath, cp) == ""
}

// removeCreatedLeaf removes the leaf of a create as steps A and C of
// undoFailedCheckout do, each after leafSafeToRemove. It deletes no
// registration and runs no branch step. A step that fails or a guard that
// refuses ends it, and nothing is reported.
func removeCreatedLeaf(worktreePath string, cp worktreeCheckpoint) {
	if !leafSafeToRemove(worktreePath, cp) || clearWorktreeLeaf(worktreePath) != "" || !leafSafeToRemove(worktreePath, cp) {
		return
	}
	_ = removeEmptyLeaf(worktreePath)
}

// leafGitFileGone reports whether the leaf is a folder with no `.git` entry at
// all. A leaf that is gone keeps the earlier answer (claustrum's own, not
// measured).
func leafGitFileGone(worktreePath string) bool {
	if fi, err := os.Stat(worktreePath); err != nil || !fi.IsDir() {
		return false
	}
	_, err := os.Lstat(filepath.Join(worktreePath, ".git"))
	return errors.Is(err, fs.ErrNotExist)
}

// gitFileGoneText is the refusal of a create whose leaf lost its `.git` file
// during the checkout (be1c1ca5, measured on Linux). The path is in the form of
// tickPath. Its spelling for a leaf under a symbolic link is not measured.
func gitFileGoneText(worktreePath string) string {
	return "refusing to create worktree: " + tickPath(worktreePath) + " does not hold the .git file made for it"
}

// clearWorktreeLeaf is step A of undoFailedCheckout. It deletes each entry at the top
// of leaf in the order that the directory read returns them, and stops at the first
// failure. The names are not sorted: both references delete in that raw order
// (measured on Linux ext4 and macOS APFS), and the frame names the first entry that
// fails. It returns "" on success, or the text of the failure. The os.Root calls give
// the measured wording, "RemoveAll <entry>: <OS error>", and they never follow a
// symlink out of the leaf. A leaf that no longer exists has nothing to clear.
func clearWorktreeLeaf(leaf string) string {
	root, err := os.OpenRoot(leaf)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		return err.Error()
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return err.Error()
	}
	names, err := dir.Readdirnames(-1)
	_ = dir.Close()
	if err != nil {
		return err.Error()
	}
	for _, name := range names {
		if err := root.RemoveAll(name); err != nil {
			return err.Error()
		}
	}
	return ""
}

// removeEmptyLeaf is step C of undoFailedCheckout. It removes leaf, which step A
// emptied, through a root at leaf's parent, so the error reads "removeat <leaf base
// name>: <OS error>", the measured wording. A leaf that no longer exists is not a
// failure. The parent comes from the cleaned path, so a leaf sent with a trailing
// slash still names its real parent.
func removeEmptyLeaf(leaf string) error {
	leaf = filepath.Clean(leaf)
	parent, err := os.OpenRoot(filepath.Dir(leaf))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = parent.Close() }()
	if err := parent.Remove(filepath.Base(leaf)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// worktreeRegistryDir is the directory that holds repo's worktree registrations:
// <common git dir>/worktrees. For a plain repo that is <repo>/.git/worktrees. When
// repo is itself a linked worktree, its .git is a file naming its own admin dir,
// and that dir's commondir file names the main repository's git dir. A git dir
// with no commondir file (a submodule's, for example) is its own common dir.
func worktreeRegistryDir(repo string) string {
	admin := worktreeAdminDir(repo)
	if admin == "" {
		return filepath.Join(repo, ".git", "worktrees")
	}
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(repo, admin)
	}
	common := admin
	if b, err := readGitPlainFile(filepath.Join(admin, "commondir")); err == nil {
		common = strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(admin, common)
		}
	}
	return filepath.Join(common, "worktrees")
}

// openRegistrationsRoot opens the registrations folder for the rollback delete and
// the index placement of a tested registration. It is os.OpenRoot behind a seam, so
// a test can change the disk right before the open. Production never reassigns it.
var openRegistrationsRoot = os.OpenRoot

// testedRegistration is the registration that the tests after the add accepted
// (createdRegistrationRefusal), or on Windows the moved one. The zero value means that the tests
// did not run. The index of the new worktree goes into it, and a rollback removes it.
// After a failed spelling call the rollback gets the one new entry of the listing
// instead, with its identity from right after that listing (gitWorktreeCreateLocked).
type testedRegistration struct {
	// path is <registrations folder of the git directory that git answered>/<last
	// name of the gitdir value that the caller read once after the add>.
	path string
	// info is the identity of the folder at path when the tests accepted it.
	info os.FileInfo
	// held is an open handle on that folder, or nil. See acceptRegistration.
	held *os.File
	// git is the held git directory of the create, or nil. The rollback delete
	// and the placement of the index open the registrations folder through it
	// (acceptedGitDir.registrationsRoot).
	git *acceptedGitDir
	// noRegistration tells a rollback to delete no registration: the create
	// was refused before it made one (addRefused).
	noRegistration bool
}

// acceptRegistration records the registration that createdRegistrationRefusal
// answered, with the identity of its folder at this moment. On Windows, and with no
// registration, it answers the zero value.
//
// It holds the folder open until the create answers (release), as the checkpoint
// holds the leaf. The identity comes from the held handle. While that handle is
// open, a new folder does not get that identity. If the open fails, the identity
// comes from a stat of the path (not measured).
// The caller must call release.
func acceptRegistration(registration string) testedRegistration {
	if !adminRecordChecked || registration == "" {
		return testedRegistration{}
	}
	reg := testedRegistration{path: registration}
	if reg.held = holdWorktreeDir(registration); reg.held != nil {
		reg.info, _ = reg.held.Stat()
	} else {
		reg.info, _ = os.Stat(registration)
	}
	return reg
}

// release closes the handle that the registration holds. The create calls it once,
// when it answers.
func (r testedRegistration) release() {
	if r.held != nil {
		_ = r.held.Close()
	}
}

// replacedIn reports whether a folder is at name in root, the opened registrations
// folder, and is not the folder that the tests accepted. It is claustrum's own
// guard (D24). The placement of the index and the rollback delete each call it
// once, on the root that they then act through. Each leaves a replaced folder as
// it is. The test is on the opened root, not on a second lookup of the path: a
// registrations folder that is a symlink can lead elsewhere at a second lookup.
// A name whose stat fails is not "replaced": the placement or the delete then
// runs and fails by itself, with the measured texts. With no accepted identity,
// because the open and the stat of acceptRegistration both failed, every folder at
// the name counts as replaced (not measured).
//
// 89cb6289 removes the folder at that path in two cells of Linux and macOS, 2 runs
// each. In a measurement, a hook renames the new registration w1 to w1x and makes
// a new empty folder w1, before a read-tree that fails. 89cb6289 removes the empty
// w1. In a measurement, the hook renames w1 to w1x and the registration w9 of a
// live sibling worktree to w1. 89cb6289 removes that folder with the files of the
// sibling. claustrum removes nothing in both cells, and the frames are equal
// (measured on Linux and macOS).
func (r testedRegistration) replacedIn(root *os.Root, name string) bool {
	now, err := root.Stat(name)
	return err == nil && (r.info == nil || !os.SameFile(r.info, now))
}

// removeTestedRegistration deletes the registration that the tests after the add
// accepted. It is step B of undoFailedCheckout. 89cb6289 removes
// that entry in each of these states of a rollback, where the .git file of the leaf
// or the record no longer leads to it:
//
//   - Measured: the record is rewritten to /nonexistent/.git. Measured: the record
//     names a live sibling worktree.
//   - Measured: the .git file of the leaf names a folder outside the
//     repository.
//   - Measured: the .git file of the leaf names the registration w9 of a sibling. w1
//     goes, and w9 stays. In one case the record of w9 names the leaf.
//   - Measured: the .git file of the leaf is removed.
//   - Measured: in attach mode and with a worktreeRoot.
//   - Measured: the daemon has GIT_DIR of another repository X, in one case with
//     GIT_COMMON_DIR of X too. The registration that git made in X is gone.
//
// Those cases ran on Linux and macOS, apart from one (Linux only).
//
// The delete is one os.Root.RemoveAll of the entry name, through a root at the
// registrations folder. It follows no symlink out of that folder. A failed delete
// returns "RemoveAll <name>: <OS error>", and the caller then keeps the branch
// (measured). In a measurement, the record is gone and the entry has mode 0500:
// logs/HEAD goes, and HEAD, commondir and logs stay.
//
// Two guards are claustrum's own. Each answers nil and deletes nothing, so the
// frame gets no clause and the branch step runs. The home guard refuses an entry
// path that is home or holds it (D2, no honest input reaches it). The identity
// guard refuses a folder that is not the one that the tests accepted (replacedIn,
// D24). It runs after the open of the root and on that root, so the guard and the
// delete use one registrations folder. A registrations folder that does not exist
// has nothing to delete. One that is a symbolic link is refused with an error,
// so nothing is deleted and the branch stays (deleteRoot, not measured).
func removeTestedRegistration(reg testedRegistration) error {
	if homeguard.WipesHomeDir(reg.path) {
		return nil
	}
	root, err := reg.git.deleteRoot(filepath.Dir(reg.path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(reg.path)
	if reg.replacedIn(root, name) {
		return nil
	}
	return removeHeldEntry(root, name)
}

// readRegistrationsDir is os.ReadDir behind a seam, so a test can fail a listing
// of registrationNames. Production never reassigns it.
var readRegistrationsDir = os.ReadDir

// registrationNames lists the names in registry, the registrations folder of a
// git directory. listed is false when the folder cannot be read. A folder that
// does not exist has no names. The read is by the path, as the other reads of
// the create that decide a refusal.
func registrationNames(registry string) (names []string, listed bool) {
	ents, err := readRegistrationsDir(registry)
	if err != nil {
		return nil, errors.Is(err, fs.ErrNotExist)
	}
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names, true
}

// createdEntryOnly reports whether registration is the one new name of the
// registrations folder since the listing before the add: after holds exactly one
// name that before does not hold, and it is the last name of registration
// (newRegistrationName). That is not a proof that this add made the entry. before and after
// are the listings of the registrations folder right before the add and right
// after the tests of createdRegistrationRefusal.
//
// 5fd08069 refuses a create whose add left two new entries, w1 and w1b, with the
// text of otherEntryRefusal. Both entries and both branches stay, and the leaf
// holds its .git file only (measured on Linux). With no new entry, 89cb6289 answers
// that text too: the daemon has GIT_COMMON_DIR of another repository, and baseRepo
// keeps an old entry of the name whose record names the new leaf (measured on Linux
// and macOS).
func createdEntryOnly(before, after []string, registration string) bool {
	name, ok := newRegistrationName(before, after)
	return ok && name == filepath.Base(registration)
}

// otherEntryRefusal is the answer of git.worktree_create when createdEntryOnly
// fails. The create runs no checkout and rolls nothing back.
func otherEntryRefusal(worktreePath string) string {
	return fmt.Sprintf("refusing to create worktree: %s carries a .git file naming an admin entry "+
		"other than the one just created for it", parentResolvedPath(worktreePath))
}

// leafGitFileRefusal tests the .git file of the new worktree right after a good
// add, on Linux and macOS. adminDir is the path that the file names. refusal is
// the answer of the create when the file names none, and noFile says that the
// leaf has no .git entry at all. The texts are those of 5fd08069 in three cells
// (measured on Linux), where a hook changes the leaf after the add:
//
//   - Measured: the .git file is removed. "<leaf> has no .git file". The empty
//     leaf is gone at the reply (the caller removes it, undoFailedAdd).
//   - Measured: a folder is at .git. "<leaf>/.git is not a regular file".
//   - Measured: the file holds `gitdir:<path>` with no blank after the colon.
//     "<leaf>/.git does not name a git dir".
//
// In each cell the registration and the branch stay. The texts name the leaf in
// the spelling of parentResolvedPath.
//
// claustrum's own, not measured: an lstat that fails with another error than "does not exist" gets the
// "not a regular file" text, and a file that cannot be read or that names an empty path gets the "does
// not name a git dir" text.
func leafGitFileRefusal(worktreePath string) (adminDir, refusal string, noFile bool) {
	leafText := parentResolvedPath(worktreePath)
	gitFile := filepath.Join(worktreePath, ".git")
	fi, err := os.Lstat(gitFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Sprintf("refusing to create worktree: %s has no .git file", leafText), true
	case err != nil || !fi.Mode().IsRegular():
		return "", fmt.Sprintf("refusing to create worktree: %s is not a regular file", filepath.Join(leafText, ".git")), false
	}
	b, err := readGitPlainFile(gitFile)
	if err == nil {
		if rest, ok := strings.CutPrefix(string(b), "gitdir: "); ok {
			adminDir = strings.TrimSpace(rest)
		}
	}
	if adminDir == "" {
		return "", fmt.Sprintf("refusing to create worktree: %s does not name a git dir", filepath.Join(leafText, ".git")), false
	}
	return adminDir, "", false
}

// createdRegistration is the registration that createdWorktreeAdminDir verified. The
// zero value means that no registration is safe to delete.
type createdRegistration struct {
	// resolved is the admin dir, and registry is the registrations directory of the
	// repo, both with their symlinks resolved. They are the two paths that the check
	// compared, and the delete acts on them.
	resolved, registry string
	// spelled is the registrations directory as the repo names it, with no
	// symlink of its own resolved. On Linux and macOS the delete opens it
	// with openRegistrationsFolder, so a link put there after the check gets
	// no delete.
	spelled string
	// registryInfo is the identity of registry at the time of the check. On Linux
	// and macOS the delete compares it with the directory that it opened.
	registryInfo os.FileInfo
	// unreadable stands for the permission error of a back-pointer that the check cannot
	// read, or for a registrations folder that is a symbolic link. No other field is
	// set then. On Linux and macOS the rollback reports it
	// as a registration that stays, and deletes nothing.
	unreadable error
}

// absoluteAdminDir makes admin, the gitdir value of the .git file of worktreePath,
// an absolute path. A relative value (git writes one with
// worktree.useRelativePaths) counts from the leaf with its symlinks resolved, as
// the kernel reads it. The leaf as sent can reach its folder through a symlink at
// another depth, and the ".." parts then lead to another folder.
func absoluteAdminDir(worktreePath, admin string) string {
	if filepath.IsAbs(admin) {
		return admin
	}
	return filepath.Join(pathcanon.Canonical(worktreePath), admin)
}

// createdWorktreeAdminDir returns the admin dir (registration) of the worktree that
// git.worktree_create built at worktreePath, or the zero value when it is not safe
// to delete. The admin dir must point back at this worktree and resolve strictly
// inside the repo's registrations directory. When baseRepo is a linked worktree,
// that is the main repository's one.
//
// Every test reads the resolved path, and the delete acts on that same path. The
// path as the .git file spells it can hold a "link/.." pair. On Linux and macOS a
// lexical clean of such a path names another folder than the kernel does.
func createdWorktreeAdminDir(repo, worktreePath string) createdRegistration {
	adminDir := worktreeAdminDir(worktreePath)
	if adminDir == "" {
		return createdRegistration{}
	}
	adminDir = absoluteAdminDir(worktreePath, adminDir)
	// A registrations folder that is a symbolic link holds nothing safe to
	// delete (registrationsLinked). The rollback then keeps the registration
	// and the branch and names the link, as with an answer of git
	// (removeTestedRegistration). Not measured for a create.
	if dir := worktreeRegistryDir(repo); registrationsLinked(dir) {
		return createdRegistration{unreadable: errors.New(realDirText(dir))}
	}
	reg := createdRegistration{resolved: pathcanon.Canonical(adminDir), registry: pathcanon.Canonical(worktreeRegistryDir(repo)),
		spelled: worktreeRegistryDir(repo)}
	// The identity comes first, so it is that of the directory that the read of the
	// back-pointer goes through.
	info, statErr := os.Stat(reg.registry)
	belongs, readErr := worktreeAdminBelongsTo(reg.resolved, worktreePath)
	under := pathStrictlyUnder(reg.resolved, reg.registry)
	if under && errors.Is(readErr, fs.ErrPermission) {
		// No delete is attempted: the registration is not verified. The error has the
		// shape of a failed delete, because the frame of 89cb6289 in this state ends
		// "(RemoveAll w1: permission denied)" (measured on macOS).
		errno := readErr
		var pe *fs.PathError
		if errors.As(readErr, &pe) {
			errno = pe.Err
		}
		return createdRegistration{unreadable: &fs.PathError{Op: "RemoveAll", Path: filepath.Base(reg.resolved), Err: errno}}
	}
	if !belongs || !under || statErr != nil {
		return createdRegistration{}
	}
	reg.registryInfo = info
	return reg
}

// verifyCreatedWorktree confirms `git worktree add` populated the very directory
// claustrum created, guarding against a directory — or one of its ancestors —
// swapped between the pre-create checks and the add. This is claustrum's own
// defensive post-condition; without it claustrum answered {"success":true} even when
// the worktree had landed on a swapped path. It returns "" when the worktree is sound,
// or a descriptive message when it is not.
//
// Reaching a non-empty return needs a concurrent swap during the add — the repo's
// own hooks are pinned off, so no honest input gets here. The frame of the test
// after the checkout is claustrum's own and NOT a byte-parity claim (the
// frame battery cannot exercise it and the exact errorCode was not measured); it
// exists so a raced create fails loudly instead of reporting a false success.
// On Linux and macOS the create also runs this test before the checkout, with
// the text and the code of 5fd08069 (measured on Linux).
//
// git worktree add populates the pre-created leaf in place, preserving its identity
// (measured), so an unraced create always matches and passes.
func verifyCreatedWorktree(worktreePath string, cp worktreeCheckpoint) string {
	if cp.info == nil {
		return "" // nothing captured — do not invent a failure
	}
	// The path must still resolve to the same location; an ancestor swapped for a
	// symlink would carry it elsewhere.
	if resolved, err := filepath.EvalSymlinks(worktreePath); err != nil || resolved != cp.resolved {
		return fmt.Sprintf("%s no longer leads to the directory that was created for the worktree", worktreePath)
	}
	after, err := os.Stat(worktreePath)
	if err != nil || !after.IsDir() {
		return fmt.Sprintf("%s is no longer the directory that was created for the worktree", worktreePath)
	}
	if !os.SameFile(cp.info, after) {
		return fmt.Sprintf("%s was not populated by git worktree add", worktreePath)
	}
	return ""
}

// gitDirRegistryDir is the directory that holds the worktree registrations of the
// git directory gitDir: <common git dir>/worktrees. The commondir file of gitDir
// names the common git directory, as for a linked worktree. Without that file,
// gitDir is its own common directory. A commondir that is not a regular file, a
// FIFO for example, counts as no file, with no wait (readGitPlainFile, not measured).
func gitDirRegistryDir(gitDir string) string {
	common := gitDir
	if b, err := readGitPlainFile(filepath.Join(gitDir, "commondir")); err == nil {
		common = strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
	}
	return filepath.Join(common, worktreesSubdir)
}

// createdRegistrationRefusal tests the .git file of the new worktree of
// git.worktree_create. refusal is the answer when that file does not name a
// registration of the repository, or "" when it does. gitDir is the git directory
// of baseRepo, as git answered it before the add. adminDir is the gitdir value of
// that .git file (worktreeAdminDir). The caller reads it once. This function does
// not read the .git file. The call runs right after a successful add.
//
// registration is the folder that gets the index of the new worktree. It is "" with
// a refusal and with no adminDir. On Linux and macOS it is the entry of test 2: the
// registration of gitDir with the last name of adminDir, not the path that adminDir
// names. With a value that names a folder that does not exist, 89cb6289 answers
// success. The registration that git made then holds the index (measured on Linux
// and macOS). For a value that git wrote with no daemon GIT_COMMON_DIR, the two are
// the same folder. The folder comes from the one gitDirRegistryDir call of the
// tests. So the commondir file of gitDir is read once, and the index folder is the
// folder that test 3 saw. On Windows no test runs, and registration is the path
// that adminDir names (absoluteAdminDir, not measured).
//
// claustrum runs four tests.
// They fit the frames and the disk of 89cb6289 in these rows (Linux with git
// 2.43, macOS with git 2.50):
//
//  1. The folder that holds the named path has the name "worktrees". The path
//     <F>/alt/worktrees/w1 passes (measured), and so does a path that starts
//     elsewhere, /elsewhere/worktrees/w1. <F>/WTREG/w1 and <F>/alt/WORKTREES/w1
//     do not pass (measured on Linux and macOS). The named path need not exist
//     (measured).
//
//  2. The registrations directory of gitDir exists, and it holds an entry with the
//     last name of the path. That entry is a real folder (measured). With
//     GIT_COMMON_DIR of another repository in the daemon environment, git makes
//     the registration in that repository, and baseRepo has none. Folder absent:
//     baseRepo has no worktrees folder at all, and the answer is the "was not
//     populated" text (measured, and on macOS only:
//     on Linux the add fails). Folder present, no entry of the name: 89cb6289
//     answers the "does not name" text (measured on Linux and macOS). The old
//     entry of cell P-p is gone at the reply there. Its record names the .git of
//     the new leaf with a slash at its end, so the step before the add removes it
//     (dropStaleWorktreeRegistration). Not measured: a worktrees folder that holds
//     entries of other names and none of this name. claustrum answers the "does
//     not name" text there, from this rule. With GIT_DIR of that repository too,
//     the create succeeds (measured with git.worktree_remove, 89cb6289, Linux and
//     macOS). From the code: git answers that repository as the git directory.
//
//  3. The commondir file of that registration names the common git directory, by
//     text. A file that holds "../../x" does not (measured), and neither does an
//     absolute path through a symlink. The path is joined as it is spelled. So a
//     registrations directory that is a symlink passes with "../.." (measured on
//     Linux). In the state shadow such a create is refused before the add
//     (worktreesFolderRefusal), and so is one with "shadowDirOff":true
//     (offWorktreesRefusal).
//
//  4. The gitdir record of that registration can be read as a file. A FIFO, a
//     folder or no file in its place cannot (measured). Cell P-h has a FIFO as the
//     commondir file and a record of another worktree, and it gets this text, where
//     cell P-c gets that of adminRecordRefusal. So test 3 comes before the record,
//     and a commondir file that cannot be read fails test 3.
//
// Tests 1, 3 and 4 answer the "does not name" text. No read waits on a FIFO
// (readGitPlainFile): 89cb6289 answers cells P-g and P-h in under 1 s. Cells P-g
// to P-j are those of 89cb6289 on Linux with git 2.43 and macOS with git 2.50. In
// each of them nothing changes after the add.
//
// Three more cells start from the state of cell P-c, and 89cb6289 answers the "does
// not name" text in each, with nothing changed after the add (Linux with git 2.43,
// macOS with git 2.50). In cell P-n the commondir file of the old registration is
// missing, which fails test 3. In cell P-o its gitdir record has mode 0000, which
// fails test 4. In cell P-q the folder of the old registration has mode 0000. Its
// stat still answers, so test 2 passes, and the commondir read fails test 3.
//
// Not measured: a path that fails test 1 and test 2 together (claustrum runs test 1
// first). Not measured either: a registrations directory or an entry whose stat
// fails with another error than "does not exist", for example a registrations
// directory with no search permission, or a file in its place. Test 2 passes there,
// the commondir read fails, and claustrum answers the "does not name" text. A
// registrations directory that is a symlink to nothing counts as absent (from the
// code, not measured). The .git file of the leaf has its own tests first
// (leafGitFileRefusal).
//
// Attach mode and a worktreeRoot take the same tests. On macOS with git 2.50,
// 89cb6289 answers the "does not name" text there too, with a registrations
// directory that is a symlink to <F>/WTREG (measured on macOS). On Linux with git 2.43 both
// cells succeed: git writes the path through the link. 89cb6289 answers that text
// also for /elsewhere/WTREG/w1, a path that does not exist (measured on Linux and
// macOS). A baseRepo that is a linked worktree creates, with the registration and
// the index in the main repository (measured on Linux and macOS). The test is off
// on Windows (adminRecordChecked), which is not measured.
//
// Three tests follow these four: adminRecordMismatch, then createdEntryOnly,
// then the identity of the leaf. The four tests and adminRecordMismatch come
// before the deadline test of the create (measured on Linux and macOS). From the
// code, the two last tests do too (not measured).
func createdRegistrationRefusal(gitDir, worktreePath, adminDir string) (registration, refusal string) {
	if adminDir == "" {
		return "", ""
	}
	if !adminRecordChecked {
		return absoluteAdminDir(worktreePath, adminDir), ""
	}
	// The texts of the tests after the add name the leaf in the spelling of
	// parentResolvedPath.
	leafText := parentResolvedPath(worktreePath)
	notOurs := fmt.Sprintf("refusing to create worktree: %s carries a .git file that does not name this "+
		"repository's own worktree admin directory", leafText)
	admin := filepath.Clean(adminDir)
	if filepath.Base(filepath.Dir(admin)) != worktreesSubdir {
		return "", notOurs
	}
	registry := gitDirRegistryDir(gitDir)
	registration = filepath.Join(registry, filepath.Base(admin))
	if _, err := os.Stat(registry); errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Sprintf("refusing to create worktree: %s was not populated by git worktree add", leafText)
	}
	// The entry must be a real folder. With a symbolic link there, to the folder
	// that was the entry, 5fd08069 answers this text and removes nothing: the link
	// and its target stay (measured on Linux, 3 of 3 runs).
	if fi, err := os.Lstat(registration); errors.Is(err, fs.ErrNotExist) || (err == nil && !fi.IsDir()) {
		return "", notOurs
	}
	b, err := readGitPlainFile(filepath.Join(registration, "commondir"))
	if err != nil {
		return "", notOurs
	}
	named := strings.TrimSpace(string(b))
	if !filepath.IsAbs(named) {
		named = filepath.Join(registration, named)
	}
	// claustrum compares by text. With an absolute path that reaches the git
	// directory through a symbolic link, 5fd08069 answers this text and removes
	// nothing (measured on Linux).
	if named != filepath.Dir(registry) {
		return "", notOurs
	}
	if readAdminRecord(registration, worktreePath) == recordUnreadable {
		return "", notOurs
	}
	return registration, ""
}

// adminRecord is what the gitdir record of a registration says about a worktree.
type adminRecord int

const (
	// recordNotCompared: the worktree path is relative or does not resolve.
	recordNotCompared adminRecord = iota
	// recordUnreadable: the record cannot be read as a regular file.
	recordUnreadable
	// recordNamesLeaf: the record names <worktree>/.git.
	recordNamesLeaf
	// recordOfAnother: the record was read and names anything else.
	recordOfAnother
)

// readAdminRecord reads the gitdir record of the registration admin and compares it
// with <worktreePath>/.git. worktreePath is taken after filepath.EvalSymlinks, which
// keeps the spelling of every component that is not a symlink. The compare is byte
// for byte, after the newlines at the end of the record are cut.
//
// With cleaned the blanks at both ends of the record are cut, and the path is
// cleaned by text (filepath.Clean). 5fd08069 creates the worktree with the record
// <leaf>/.git/ (measured on Linux), and with a blank at its end, with
// <leaf>/./.git and with <leaf>//.git (measured on Linux). The record stays as
// written. claustrum's own, not measured: a blank at the start, a tab, a CR and
// an "x/.." pair also name the leaf. A record of another worktree stays one
// (measured). The create sets cleaned only after an answer of git to rev-parse
// --absolute-git-dir. Without that answer the compare is as before (not
// measured).
//
// A relative record counts from admin with its symlinks resolved. git writes one
// with worktree.useRelativePaths, and 89cb6289 creates with such a record
// (measured on macOS). A relative record of another worktree is a record of
// another worktree (measured), and so is an empty file. A record that is not a
// regular file fails at once (readGitPlainFile).
func readAdminRecord(admin, worktreePath string) adminRecord {
	return compareAdminRecord(admin, worktreePath, false)
}

// compareAdminRecord is readAdminRecord with the choice of cleaned.
func compareAdminRecord(admin, worktreePath string, cleaned bool) adminRecord {
	b, err := readGitPlainFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return recordUnreadable
	}
	if !filepath.IsAbs(worktreePath) {
		return recordNotCompared
	}
	resolved, err := evalSymlinks(worktreePath)
	if err != nil {
		return recordNotCompared
	}
	record := strings.TrimRight(string(b), "\n")
	if cleaned {
		record = filepath.Clean(strings.TrimSpace(record))
	}
	if !filepath.IsAbs(record) {
		record = filepath.Join(pathcanon.Canonical(admin), record)
	}
	if record == filepath.Join(resolved, ".git") {
		return recordNamesLeaf
	}
	return recordOfAnother
}

// adminRecordMismatch reports whether the `gitdir` record of the registration admin
// was read and names another path than <worktreePath>/.git (readAdminRecord). The
// caller gets admin from createdRegistrationRefusal, or from absoluteAdminDir if
// rev-parse gave no answer: it is the folder that gets the index of the new worktree.
// 89cb6289 and f6010b97 refuse such a create on macOS when the request spells the folder in Unicode
// NFD and git records it in NFC (measured). They run no checkout and roll nothing back. The same
// request in NFC for an NFD folder succeeds (measured). A measurement is not measured on Linux. Cells
// P-c, P-f and P-k to P-m measured this test there. On Linux every create of rows CSa to CSd succeeded
// on both references and on claustrum.
//
// The record is that of the entry in the registrations directory of baseRepo, not that of the folder that the .git file
// names. A measurement shows it (89cb6289, Linux with git 2.43 and macOS with git 2.50). There the daemon has GIT_COMMON_DIR of
// another repository X, and git makes the registration in X. Its record names the new leaf. baseRepo holds an old entry of
// the same name, and the record of that entry names another worktree. 89cb6289 refuses with the text of adminRecordRefusal
// and changes nothing: the index of the old entry keeps its bytes. With no worktrees folder in baseRepo, the answer is the
// "was not populated" text (measured). A measured state with an old registration, with a timeoutMs that expired during the
// add, gets the same refusal, and the leaf and the branch stay (measured on Linux and macOS). So the create runs this test
// before its deadline test.
//
// Three more cases start from that state and get the same refusal, with nothing
// changed (`89cb6289`, measured on Linux and macOS). In one the old record is the
// relative path ../../../gone/w1/.git. In another it is an empty file. In cell
// P-m the old entry also holds a `locked` file. A record that cannot be read as a
// file gets the other text first (createdRegistrationRefusal, cells P-g, P-i and
// P-j).
//
// Not measured: the raw bytes of the record (the NFC spelling was read from `git
// worktree list`), whether the compare is bytewise (inferred from two measured cases
// together), and the resolution of symlinks before the compare. A symlinked path
// such as /tmp on macOS creates as usual on both references (measured), so claustrum
// resolves them. A worktreePath that is relative or does not resolve gives no
// mismatch. The check is off on Windows (adminRecordChecked). That is claustrum's
// choice.
func adminRecordMismatch(admin, worktreePath string, cleaned bool) bool {
	return adminRecordChecked && admin != "" && compareAdminRecord(admin, worktreePath, cleaned) == recordOfAnother
}

// adminRecordRefusal is the answer of git.worktree_create when adminRecordMismatch
// reports a mismatch. It names worktreePath in the spelling of parentResolvedPath
// (measured on Linux and macOS). A component that is not a symlink keeps its spelling
// as sent, as.
func adminRecordRefusal(worktreePath string) string {
	return fmt.Sprintf("refusing to create worktree: %s carries a .git file naming an admin directory "+
		"whose own record is of a different worktree", parentResolvedPath(worktreePath))
}

// spellingRetryText is the answer of git.worktree_create when adminRecordMismatch
// reports a mismatch and git then fails the `rev-parse --show-toplevel` call of
// gitDirWorkTreeToplevel. registration is the entry that the tests accepted. The
// code is worktree_add_failed. 5fd08069 answers this text and removes the leaf,
// the registration w1 and the new branch (measured on Linux). When the call
// works, it answers adminRecordRefusal and removes nothing (measured there).
func spellingRetryText(registration string) string {
	return "git worktree add failed (verification): could not verify the worktree's registration: " +
		"git did not report its spelling of the path (entry " + filepath.Base(registration) + "); retry"
}

// parentResolvedPath is a path as some refusals of git.worktree_create name it
// on Linux and macOS. The path is cleaned, the symlinks of its parent folder are
// resolved, and its last name is kept as it is. The rule fits these cells of
// 89cb6289 (measured on Linux and macOS):
//
//   - A baseRepo behind a symlink, with both request paths through the link. The
//     link is resolved in the "does not name" text (measured), the
//     "different worktree" text (measured), the "other than the one just created"
//     text, the "was not populated" text and the "already exists" text.
//   - A leaf that is a symlink to a folder or to a file, or a dangling symlink, in
//     the "already exists" text. The last name stays, and the target is not named
//     (measured). A leaf that is a regular file: measured.
//   - A path that is not clean, with a slash at its end, a double slash or a "/./"
//     part. The cleaned path is named (measured for "already exists" and
//     for the four other texts).
//   - A worktreeRoot behind a symlink. The link is resolved in the "already
//     exists" text (measured), in the "different worktree" text and in the "is
//     not marked as a worktree directory" text, which names the folder that holds
//     the leaf.
//
// 89cb6289 names the leaf as sent in the undo clause of a rollback (measured), in the
// path of a success, in the text of git and in the locked refusal of
// git.worktree_remove, on Linux and macOS. Those keep the path as sent. The "is not
// inside the repository" text names the path as sent too (measured). Every other
// refusal of the create keeps its spelling: no cell measured it.
//
// On Windows no create reaches this function: the four tests after the add do not
// run there, and the other two texts have their own spelling (existingPathSpelling
// and externalPathSpelling). Windows is not measured for 89cb6289.
//
// A parent folder that does not resolve gives the cleaned path as sent (not
// measured).
func parentResolvedPath(path string) string {
	path = filepath.Clean(path)
	parent, err := evalSymlinks(filepath.Dir(path))
	if err != nil {
		return path
	}
	return filepath.Join(parent, filepath.Base(path))
}

// registeredLeafRefusal answers a create at a leaf that an entry of
// <commonDir>/worktrees still names, or "" when no entry names it. The leaf must be
// gone or an empty folder, the two states in which git refuses a registered path.
// The texts are those of be1c1ca5, measured on Linux, macOS and Windows. A path
// with a character of genericHintChars gets the hint with no path. Not measured: a
// `'` on Windows, and every other character in the hint.
func registeredLeafRefusal(commonDir, worktreePath string) string {
	if fi, err := os.Lstat(worktreePath); err == nil {
		if !fi.IsDir() || !emptyFolder(worktreePath) {
			return ""
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	name, lock := registeredLeafEntry(commonDir, newWorktreePathSet(worktreePath, parentResolvedPath(worktreePath)))
	if name == "" {
		return ""
	}
	head := "cannot add a worktree: " + tickPath(worktreePath) + " is still registered in this repository as "
	switch {
	case lock == notLocked:
		return head + `a worktree (entry "` + name + `"); clear the stale registration first (git worktree prune), then try again`
	case strings.ContainsAny(worktreePath, genericHintChars):
		return head + `a locked worktree (entry "` + name + `"); unlock it with git worktree unlock, giving the folder's ` +
			`path (git worktree list shows it), then git worktree prune, or use another folder`
	default:
		return head + `a locked worktree (entry "` + name + `"); unlock it first (` + unlockCommand(worktreePath) +
			`, then git worktree prune), or use another folder`
	}
}

// emptyFolder reports whether the folder dir holds no entry.
func emptyFolder(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	_, err = f.Readdirnames(1)
	return errors.Is(err, io.EOF)
}
