package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// worktreeCheckpoint records how the freshly created worktree leaf looked right
// after mkdirWorktreeLeaf, so verifyCreatedWorktree can confirm `git worktree add`
// populated that same directory rather than one swapped in underneath it.
type worktreeCheckpoint struct {
	info     os.FileInfo // identity (dev+inode via os.SameFile) of the created leaf
	resolved string      // the leaf path with every symlink component resolved
}

// checkpointCreatedWorktree captures the leaf's identity for the post-add check.
// A capture failure yields an empty checkpoint, which verifyCreatedWorktree treats
// as "nothing to compare against" and passes — the verification is a best-effort
// guard, never a new way to fail an honest create.
func checkpointCreatedWorktree(worktreePath string) worktreeCheckpoint {
	info, err := os.Stat(worktreePath)
	if err != nil {
		return worktreeCheckpoint{}
	}
	resolved, err := filepath.EvalSymlinks(worktreePath)
	if err != nil {
		return worktreeCheckpoint{}
	}
	return worktreeCheckpoint{info: info, resolved: resolved}
}

// undoCreatedWorktree rolls back a worktree that git.worktree_create built but whose
// caller timeoutMs was exceeded by the post-checkout pipe drain — reproducing 4534d86,
// which deletes the branch and removes the worktree before answering errorCode "timeout".
// Best-effort: the reply is the timeout frame regardless of whether every step lands.
//
// worktreePath has already passed git.worktree_create's containment checks by the time
// a checkout runs (strictly inside repo, or 2-level inside an external worktreeRoot;
// never home), so removing it here is safe. The os.RemoveAll is the fallback that makes
// the removal observable even when `git worktree remove` cannot finish the job. Because
// the delete runs seconds after the leaf was created (the drain), the fallback re-checks
// the always-on home guard and the leaf's checkpoint identity first, so a swap during
// the drain cannot redirect it onto a replacement's contents.
func undoCreatedWorktree(repo, worktreePath, branch string, cp worktreeCheckpoint) {
	if branch != "" {
		hardenedGit(repo, false, "update-ref", "--no-deref", "-d", "refs/heads/"+branch)
	}
	hardenedGit(repo, false, "worktree", "remove", "--force", worktreePath)
	// Prune the registration, guarded exactly as gitWorktreeRemove does: the admin dir
	// must point back at this worktree and resolve strictly inside <repo>/.git/worktrees.
	if adminDir := worktreeAdminDir(worktreePath); adminDir != "" &&
		worktreeAdminBelongsTo(adminDir, worktreePath) &&
		pathStrictlyUnder(canonicalPath(adminDir), canonicalPath(filepath.Join(repo, ".git", "worktrees"))) {
		_ = os.RemoveAll(adminDir)
	}
	// The os.RemoveAll fallback is an RPC-supplied path reaching a recursive delete, so it
	// owes the always-on home guard (AGENTS.md / D2), defense-in-depth exactly as
	// gitWorktreeRemove applies it — even though create's containment already refused home.
	if worktreePath != "" && wipesHomeDir(worktreePath) {
		return
	}
	// Re-confirm the leaf is still the very directory create made before deleting it: a
	// concurrent swap during the drain must not redirect this RemoveAll onto a replacement.
	// verifyCreatedWorktree returns non-empty on a swap (and when `git worktree remove`
	// already deleted the leaf, in which case the RemoveAll would be a no-op anyway); an
	// empty checkpoint has nothing to compare and falls back to the guards above.
	if msg := verifyCreatedWorktree(worktreePath, cp); msg != "" {
		return
	}
	_ = os.RemoveAll(worktreePath)
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
