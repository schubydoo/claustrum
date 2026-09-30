package main

import (
	"path/filepath"
	"sync"
)

// worktreeRepoLocks serialises worktree operations per repository within this
// daemon. A connection's requests dispatch concurrently, so two
// git.worktree_create/_remove calls on the SAME repo could run at once and race on the
// shared worktree state — `git worktree add`/`remove` and the bookkeeping under
// .git/worktrees, including dropStaleWorktreeRegistration's non-atomic
// readdir-then-remove. Different repositories keep running in parallel.
//
// This is an in-process sync.Mutex, not a filesystem lock: it serialises within one
// daemon, not across daemons that share a repo. A single operation is byte-identical
// with or without it — this only closes a same-repo race window under concurrency.
var worktreeRepoLocks sync.Map // canonical repo path -> *sync.Mutex

// withWorktreeRepoLock runs fn while holding the per-repo worktree lock.
func withWorktreeRepoLock(repo string, fn func() response) response {
	m, _ := worktreeRepoLocks.LoadOrStore(canonicalPath(repo), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// worktreeBranchLocks serialises the branch step (worktreebranch.go) per common git
// dir. The lock of withWorktreeRepoLock is keyed by baseRepo. So it does not cover
// two removes through the repository and through one of its linked worktrees.
// 89cb6289 runs those two branch steps one after the other (row R21b, Linux VM). The key is the
// git dir under whose worktrees directory the entries are removed (verifyGitDir).
// That key is claustrum's choice (not measured): the rows do not tell the common dir
// from the main worktree. A create rollback takes the same lock (not measured). The
// branch lock is always taken last, so it does not deadlock with the per-repo lock.
var worktreeBranchLocks sync.Map // canonical common git dir -> *sync.Mutex

// lockWorktreeBranchStep takes the branch lock of repo and returns its unlock.
func lockWorktreeBranchStep(repo string) func() {
	key := verifyGitDir(repo)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	m, _ := worktreeBranchLocks.LoadOrStore(canonicalPath(key), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
