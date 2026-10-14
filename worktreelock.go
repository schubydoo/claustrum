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

// worktreeCreateLocks serialises git.worktree_create per git directory. The lock
// of withWorktreeRepoLock is keyed by baseRepo, so the main repository and a
// linked worktree of it get two locks there. 5fd08069 runs a create through the
// main repository and a create through a linked worktree of it one after the
// other. With the first `worktree add` held for 2 s, the second add starts after
// the first one ended, and both creates succeed (cell T1 of round tier0-11,
// Linux VM, 3 of 3 runs). The key is that of the branch lock
// (worktreeGitDirKey). With a GIT_COMMON_DIR in the environment of the daemon
// that key is <baseRepo>/.git, so the two creates get two locks (not measured).
//
// Two different repositories run at the same time, as on 5fd08069 (cell T2 of
// round tier0-12, Linux VM). A remove does not take this lock. 5fd08069 answers a
// remove through a linked worktree after the held add of a create, and claustrum
// answers it during that add (cell T3 of the same round).
var worktreeCreateLocks sync.Map // canonical git dir -> *sync.Mutex

// withWorktreeCreateLock runs fn, a git.worktree_create, while it holds the
// per-repo lock of repo and then the create lock of its git directory. The order
// is always the per-repo lock, the create lock, the branch lock, so the three do
// not deadlock.
func withWorktreeCreateLock(repo string, fn func() response) response {
	return withWorktreeRepoLock(repo, func() response {
		m, _ := worktreeCreateLocks.LoadOrStore(worktreeGitDirKey(repo), &sync.Mutex{})
		mu := m.(*sync.Mutex)
		mu.Lock()
		defer mu.Unlock()
		return fn()
	})
}

// worktreeGitDirKey is the key of the create lock and of the branch lock of repo:
// the git dir under whose worktrees directory the entries are (verifyGitDir), as
// a canonical path. The main repository and its linked worktrees get one key.
func worktreeGitDirKey(repo string) string {
	key := verifyGitDir(repo)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	return canonicalPath(key)
}

// worktreeBranchLocks serialises the branch step (worktreebranch.go) per common git
// dir. The lock of withWorktreeRepoLock is keyed by baseRepo. So it does not cover
// two removes through the repository and through one of its linked worktrees.
// 89cb6289 runs those two branch steps one after the other (row R21b, Linux VM). The
// key is the git dir under whose worktrees directory the entries are removed
// (verifyGitDir).
// That key is claustrum's choice (not measured): the rows do not tell the common dir
// from the main worktree. A create rollback takes the same lock (not measured). The
// branch lock is always taken last, so it does not deadlock with the per-repo lock.
var worktreeBranchLocks sync.Map // canonical common git dir -> *sync.Mutex

// lockWorktreeBranchStep takes the branch lock of repo and returns its unlock.
func lockWorktreeBranchStep(repo string) func() {
	m, _ := worktreeBranchLocks.LoadOrStore(worktreeGitDirKey(repo), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
