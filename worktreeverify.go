package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// /proc/<pid>/fd on a Linux VM and with handle64 on a Windows VM). The caller must
// call release.
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
// on macOS and Windows VMs. A retry at the same path therefore succeeds after the
// common failures (a branch that already exists, a ref lock), which leave the leaf
// empty and unregistered.
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
// on Linux and macOS VMs.
func undoFailedAdd(worktreePath string, cp worktreeCheckpoint) {
	if worktreePath == "" || wipesHomeDir(worktreePath) {
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
// a failed read-tree checkout, a failed placement of the index (Linux and macOS),
// or after the caller's timeoutMs expired during the
// add, the checkout, a checkout drain that overran the drain cap, or the copy step.
// It returns "" when the undo finished, or the text that the caller appends to the
// frame's error. The errorCode of the frame does not change. kept is true when the
// branch step kept the branch because it holds commits that no other ref reaches.
// The frame then carries "branchKept":true. The steps and the leaf texts were
// measured against f6010b97 and 90fca6e6 on a Windows VM:
//
//   - Step A deletes the entries at the top of the leaf, one at a time, in the order
//     that the directory read returns them, not sorted. It stops at the first entry
//     that it cannot delete. Then the text is
//     "; and the undo could not finish for <leaf>: the worktree directory, its
//     registration, and the branch all remain; remove them by hand before retrying
//     (RemoveAll <entry>: <OS error>)", and nothing else is undone. In attach mode
//     the call made no branch, and the text reads "the worktree directory and its
//     registration both remain" instead (f6010b97 and 89cb6289, row C08b).
//   - Step B deletes the worktree's registration, then runs the branch step
//     (runBranchStep) on the branch that the call created. In attach mode branch is
//     "", so no git call runs. After the attach fallback, the call created
//     branchName. The caller's expired deadline does not stop the branch step (rows
//     C04 and B2-10). If the registration cannot be deleted, the branch step does
//     not run, step C still runs, and the text is "; and the undo could not finish
//     for <leaf>: the worktree registration and the branch remain; remove them by
//     hand before retrying (RemoveAll <registration name>: <OS error>)". 89cb6289
//     gives it after a failed placement of the index, with the leaf gone (rows A14
//     and A14f on a Linux VM, A14 and A14b on a macOS VM). In attach mode the text
//     reads "the worktree registration remains; remove it by hand before retrying
//     (...)" instead (cell X5, Linux and macOS VMs). A step C that fails too adds
//     no text of its own (cell X11, Linux VM). The same texts after a timeout, and
//     after a failed read-tree checkout, are claustrum's choice (not measured). On Windows a registration that cannot be deleted adds no text, and
//     the branch step runs (removeCreatedRegistration): Windows is not measured.
//   - Step C removes the leaf directory, which is now empty. If that fails, the text
//     is "; and the undo could not finish for <leaf>: the worktree directory remains
//     (re-populated while undoing?); remove it by hand before retrying (removeat
//     <leaf base name>: <OS error>)".
//
// A branch that the branch step keeps adds its own part (rollbackBranchText) after
// the leaf part, joined by "; " (row C05). A kept branch together with a failed rmdir
// is the only pair that a row measured. Any other pair of the two parts uses the same
// join. That is claustrum's choice (not measured).
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
func undoFailedCheckout(repo, worktreePath, branch string, cp worktreeCheckpoint) (text string, kept bool) {
	// The admin dir is found through the leaf's .git file, which step A deletes.
	registration := createdWorktreeAdminDir(repo, worktreePath)
	leafSafe := func() bool {
		return worktreePath != "" && !wipesHomeDir(worktreePath) && verifyCreatedWorktree(worktreePath, cp) == ""
	}
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
	// A registration that cannot be deleted stays, and then the branch step does not
	// run: 89cb6289 runs no git call after it (row A14, Linux VM).
	var res branchStepResult
	regErr := removeCreatedRegistration(registration)
	if regErr == nil {
		res = runBranchStep(repo, branch)
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
		// (cell X11, Linux VM). In attach mode the call made no branch (cell X5).
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

// clearWorktreeLeaf is step A of undoFailedCheckout. It deletes each entry at the top
// of leaf in the order that the directory read returns them, and stops at the first
// failure. The names are not sorted: both references delete in that raw order
// (measured on Linux ext4 and macOS APFS VMs), and the frame names the first entry
// that fails. It returns "" on success, or
// the text of the failure. The os.Root calls give the measured wording,
// "RemoveAll <entry>: <OS error>", and they never follow a symlink out of the leaf.
// A leaf that no longer exists has nothing to clear.
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
	if b, err := os.ReadFile(filepath.Join(admin, "commondir")); err == nil {
		common = strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(admin, common)
		}
	}
	return filepath.Join(common, "worktrees")
}

// createdRegistration is the registration that createdWorktreeAdminDir verified. The
// zero value means that no registration is safe to delete.
type createdRegistration struct {
	// resolved is the admin dir, and registry is the registrations directory of the
	// repo, both with their symlinks resolved. They are the two paths that the check
	// compared, and the delete acts on them.
	resolved, registry string
}

// createdWorktreeAdminDir returns the admin dir (registration) of the worktree that
// git.worktree_create built at worktreePath, or the zero value when it is not safe
// to delete. The admin dir must point back at this worktree and resolve strictly
// inside the repo's registrations directory. When baseRepo is a linked worktree,
// that is the main repository's one.
//
// Every test reads the resolved path, and the delete acts on that same path. The
// path as the .git file spells it can hold a "link/.." pair. A lexical clean of
// such a path names another folder than the kernel does.
func createdWorktreeAdminDir(repo, worktreePath string) createdRegistration {
	adminDir := worktreeAdminDir(worktreePath)
	if adminDir == "" {
		return createdRegistration{}
	}
	if !filepath.IsAbs(adminDir) {
		adminDir = filepath.Join(worktreePath, adminDir)
	}
	reg := createdRegistration{resolved: canonicalPath(adminDir), registry: canonicalPath(worktreeRegistryDir(repo))}
	if !worktreeAdminBelongsTo(reg.resolved, worktreePath) || !pathStrictlyUnder(reg.resolved, reg.registry) {
		return createdRegistration{}
	}
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
// own hooks are pinned off, so no honest input gets here. Like the recovered-panic
// reply, the frame is therefore claustrum's own and NOT a byte-parity claim (the
// frame battery cannot exercise it and the exact errorCode was not measured); it
// exists so a raced create fails loudly instead of reporting a false success.
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

// adminRecordMismatch reports whether the admin record that `git worktree add` wrote
// for the new worktree names another path than worktreePath. It reads
// <worktreePath>/.git, the admin directory that file names, and that directory's
// `gitdir` record, and compares the record with <worktreePath>/.git byte for byte.
// worktreePath is taken after filepath.EvalSymlinks, which keeps the spelling of every
// component that is not a symlink. 89cb6289 and f6010b97 refuse such a create on a
// macOS VM when the request spells the folder in Unicode NFD and git records it in
// NFC (row I07a). They run no checkout and roll nothing back. The same request in NFC
// for an NFD folder succeeds (row I07b). Linux is not measured, and claustrum checks
// there too. On a Linux VM every create of rows CSa to CSd succeeded on both
// references and on claustrum.
//
// Not measured: the raw bytes of the record (the NFC spelling was read from `git
// worktree list`), whether the compare is bytewise (inferred from I07a and I07b
// together), and the resolution of symlinks before the compare. A symlinked path
// such as /tmp on macOS creates as usual on both references (row I01e), so
// claustrum resolves them. A relative worktreePath or record, and any read that
// fails, give no mismatch. The check is off on Windows (adminRecordChecked). That is
// claustrum's choice.
func adminRecordMismatch(worktreePath string) bool {
	if !adminRecordChecked || !filepath.IsAbs(worktreePath) {
		return false
	}
	admin := worktreeAdminDir(worktreePath)
	if admin == "" {
		return false
	}
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(worktreePath, admin)
	}
	b, err := os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return false
	}
	record := strings.TrimRight(string(b), "\n")
	if !filepath.IsAbs(record) {
		return false
	}
	resolved, err := evalSymlinks(worktreePath)
	if err != nil {
		return false
	}
	return record != filepath.Join(resolved, ".git")
}

// adminRecordRefusal is the answer of git.worktree_create when adminRecordMismatch
// reports a mismatch. It names worktreePath as sent (row I07a).
func adminRecordRefusal(worktreePath string) string {
	return fmt.Sprintf("refusing to create worktree: %s carries a .git file naming an admin directory "+
		"whose own record is of a different worktree", worktreePath)
}
