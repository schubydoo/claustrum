package main

import (
	"path/filepath"
	"sync"
)

// worktreeRepoLocks serialises git.worktree_remove per repository within this
// daemon. A connection's requests dispatch concurrently, so two removes on the
// SAME repo could run at once and race on the bookkeeping under .git/worktrees.
// Different repositories keep running in parallel. A create does not take this
// lock: a remove through the main repository deletes its leaf while the add of a
// create runs (cell T3M of round tier3-02, Linux VM).
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

// worktreeCreateLocks serialises git.worktree_create per git directory. The main
// repository and a linked worktree of it get one lock. 5fd08069 runs a create
// through the main repository and a create through a linked worktree of it one
// after the other. With the first `worktree add` held for 2 s, the second add
// starts after the first one ended, and both creates succeed (cell T1 of round
// tier0-11, Linux VM, 3 of 3 runs). The key is that of the branch lock
// (worktreeGitDirKey). With a GIT_COMMON_DIR in the environment of the daemon
// that key is <baseRepo>/.git, so the two creates get two locks (not measured).
//
// Two different repositories run at the same time, as on 5fd08069 (cell T2 of
// round tier0-12, Linux VM). A remove does not take this lock. It takes the
// lock of the add step (lockWorktreeAddStep).
var worktreeCreateLocks sync.Map // canonical git dir -> *sync.Mutex

// withWorktreeCreateLock runs fn, a git.worktree_create, while it holds the
// create lock of the git directory of repo. The order is always the per-repo
// lock of a remove, the create lock, the lock of the add step, the branch lock,
// so they do not deadlock.
func withWorktreeCreateLock(repo string, fn func() response) response {
	m, _ := worktreeCreateLocks.LoadOrStore(worktreeGitDirKey(repo), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// worktreeAddLocks is the lock of the add step per git directory. A create holds
// it while its `worktree add` runs (worktreeAdd). A remove holds it from the
// delete of the registration to its reply, and not for the delete of the leaf.
// 5fd08069 shows this order in round tier3-02 (cell T3 on Linux and macOS VMs,
// cell T3M on a Linux VM): the leaf goes during a held add, and the
// registration and the branch go after it, then the reply. A create held at its
// read-tree checkout, after the add, does not delay a remove (cell T3C, Linux
// VM). The key is that of the create lock (worktreeGitDirKey). A create takes no
// branch lock while it holds this one.
var worktreeAddLocks sync.Map // canonical git dir -> *sync.Mutex

// lockWorktreeAddStep takes the lock of the add step of repo and returns its unlock.
func lockWorktreeAddStep(repo string) func() {
	m, _ := worktreeAddLocks.LoadOrStore(worktreeGitDirKey(repo), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
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
