//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
)

// dropStaleWorktreeRegistration removes the repo's worktree registration for
// worktreePath when that path is registered but missing on disk — a "prunable"
// registration left when a session folder is deleted out from under git. Without
// this, `git worktree add` at the same path fails "missing but already registered"
// where 7d193f89 prunes the stale record and recreates cleanly.
//
// Only the registration for THIS path is dropped; other prunable registrations are
// left in place (measured against 7d193f89 — a global `git worktree prune` would
// clear those too and diverge). The caller invokes this only after confirming the
// target does not exist, so a matching registration is necessarily stale.
//
// A registration lives at <repo>/.git/worktrees/<name>/gitdir and points at
// "<worktree>/.git". Both paths are compared through resolveAsFarAsExists so a
// symlinked ancestor matches: on macOS git records the RESOLVED worktree path
// (/private/var/…) in gitdir while the caller passes the unresolved one (/var/…),
// and a plain filepath.Clean would never match. Best-effort: any failure leaves git
// to report its own error.
//
// This is the Windows step. Linux and macOS have their own compare and their own
// delete (worktreestale_unix.go). No Windows row of 89cb6289 measures this step, so
// Windows keeps the compare and the delete that it had. The result is always "":
// Windows remembers no entry.
func dropStaleWorktreeRegistration(repo, worktreePath string) (kept string) {
	base := filepath.Join(repo, ".git", "worktrees")
	ents, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	target := resolveAsFarAsExists(worktreePath)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		// A record that is not a regular file is passed over at once. With a FIFO
		// there, 89cb6289 goes on to `git worktree add` (row B-G1, Linux and macOS VMs).
		gitdir, err := readGitPlainFile(filepath.Join(base, e.Name(), "gitdir"))
		if err != nil {
			continue
		}
		// gitdir names the worktree's own ".git" file; its parent is the worktree.
		if resolveAsFarAsExists(filepath.Dir(strings.TrimSpace(string(gitdir)))) == target {
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
			return ""
		}
	}
	return ""
}
