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
// "<worktree>/.git". Best-effort: any failure leaves git to report its own error.
func dropStaleWorktreeRegistration(repo, worktreePath string) {
	base := filepath.Join(repo, ".git", "worktrees")
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	target := filepath.Clean(worktreePath)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		gitdir, err := os.ReadFile(filepath.Join(base, e.Name(), "gitdir"))
		if err != nil {
			continue
		}
		// gitdir names the worktree's own ".git" file; its parent is the worktree.
		if filepath.Dir(filepath.Clean(strings.TrimSpace(string(gitdir)))) == target {
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
			return
		}
	}
}
