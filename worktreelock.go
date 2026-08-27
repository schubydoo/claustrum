package main

import "sync"

// worktreeRepoLocks serialises worktree operations per repository within this
// daemon, matching 7d193f89's withWorktreeRepoLock. A connection's requests dispatch
// concurrently, so two git.worktree_create/_remove calls on the SAME repo could run
// at once and race on the shared worktree state — `git worktree add`/`remove` and the
// bookkeeping under .git/worktrees, including dropStaleWorktreeRegistration's
// non-atomic readdir-then-remove. Different repositories keep running in parallel.
//
// Like the reference, this is an in-process sync.Mutex, not a filesystem lock: it
// serialises within one daemon, not across daemons that share a repo. A single
// operation is byte-identical with or without it — this only closes a same-repo race
// window under concurrency.
var worktreeRepoLocks sync.Map // canonical repo path -> *sync.Mutex

// withWorktreeRepoLock runs fn while holding the per-repo worktree lock.
func withWorktreeRepoLock(repo string, fn func() response) response {
	m, _ := worktreeRepoLocks.LoadOrStore(canonicalPath(repo), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}
