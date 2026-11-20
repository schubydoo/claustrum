package git

import (
	"path/filepath"
	"sync"

	"github.com/schubydoo/claustrum/internal/pathcanon"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// worktreeRepoLocks serialises git.worktree_remove per repository within this
// daemon. A connection's requests dispatch concurrently, so two removes on the
// SAME repo could run at once and race on the bookkeeping under .git/worktrees.
// Different repositories keep running in parallel. A create does not take this
// lock: a remove through the main repository deletes its leaf while the add of a
// create runs (measured on Linux).
//
// This is an in-process sync.Mutex, not a filesystem lock: it serialises within one
// daemon, not across daemons that share a repo. A single operation is byte-identical
// with or without it — this only closes a same-repo race window under concurrency.
var worktreeRepoLocks sync.Map // canonical repo path -> *sync.Mutex

// underRepoMutex runs fn while holding the per-repo worktree lock.
func underRepoMutex(repo string, fn func() rpc.Response) rpc.Response {
	m, _ := worktreeRepoLocks.LoadOrStore(pathcanon.Canonical(repo), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// worktreeCreateLocks serialises the add of git.worktree_create, per
// git directory. The main repository and a linked worktree of it get one lock.
// 5fd08069 runs a create through the main repository and a create through a
// linked worktree of it one after the other. With the first `worktree add`
// held for 2 s, the second add starts after the first one ended, and both
// creates succeed (measured on Linux, 3 of 3 runs). The key is that of the
// branch lock (worktreeGitDirKey). With a GIT_COMMON_DIR in the environment of
// the daemon that key is <baseRepo>/.git, so the two creates get two locks
// (not measured).
//
// A create takes the lock right before its `worktree add`: after the listing
// of the add and the pair before it, and before the staging folder is made
// (takeAddLocks). It ends the hold right after its second listing of the
// registrations folder, which comes after the add and its move. So the
// steps of a second create before its add run while the first add runs, and
// that create then waits. 4c111e86 runs the listings and the rev-parse calls
// of the second create during a held add of the first, and the second create
// answers the stat text of the gone copy 1 ms after that add ends (measured
// on Linux, in its call log). The steps between the end of the hold
// and the next git call of the first create are claustrum's own.
//
// Two different repositories run at the same time, as on 5fd08069 (measured
// on Linux). A remove does not take this lock. It takes the lock of the add
// step (lockWorktreeAddStep).
//
// Each rollback takes the lock first (createHold.retake) and holds it until
// the reply. So no rollback runs during the staged add of a second create, and
// it removes no registration, leaf or branch name that such an add is making
// (claustrum's own, not measured). A rollback that waits for the lock can
// answer later than timeoutMs (claustrum's own, not measured).
var worktreeCreateLocks sync.Map // canonical git dir -> *sync.Mutex

// createLockRetakes runs right before a rollback waits for the create lock. A
// test replaces it.
var createLockRetakes = func() {}

// createHold is the hold of the create lock by one git.worktree_create
// (withWorktreeCreateLock). It is used on the goroutine of that create only.
// beforeAdd runs right after the locks of an add are taken (takeAddLocks). A
// nil hold takes no create lock.
type createHold struct {
	mu        *sync.Mutex
	held      bool
	beforeAdd func()
}

// retake takes the create lock for a rollback, unless the hold has it.
func (h *createHold) retake() {
	if h == nil || h.held {
		return
	}
	createLockRetakes()
	h.mu.Lock()
	h.held = true
}

// release ends the hold of the create lock, if it has it.
func (h *createHold) release() {
	if h == nil || !h.held {
		return
	}
	h.held = false
	h.mu.Unlock()
}

// withWorktreeCreateLock runs fn, a git.worktree_create, with the hold of the
// create lock of the git directory of repo. The hold starts free: the add
// takes the lock (takeAddLocks), and each rollback takes it (retake). The
// order is always the per-repo lock of a remove, the create lock, the lock of
// the add step, the branch lock, so they do not deadlock. A rollback after
// retake holds no lock of the add step or branch lock before it.
func withWorktreeCreateLock(repo string, fn func(hold *createHold) rpc.Response) rpc.Response {
	m, _ := worktreeCreateLocks.LoadOrStore(worktreeGitDirKey(repo), &sync.Mutex{})
	hold := &createHold{mu: m.(*sync.Mutex)}
	defer hold.release()
	return fn(hold)
}

// beforeAddLocks runs right before an add waits for its locks
// (takeAddLocks), and afterAddHold right after a create ended the hold of the
// create lock after its add (endAddHold). Tests replace them. Production never
// does.
var (
	beforeAddLocks = func() {}
	afterAddHold   = func() {}
)

// takeAddLocks takes the create lock of hold and the lock of the add step of
// repo for one `worktree add`, and runs hold.beforeAdd. end ends the lock of
// the add step. The create lock stays until endAddHold. A nil hold takes the
// lock of the add step alone.
func takeAddLocks(hold *createHold, repo string) (end func()) {
	beforeAddLocks()
	if hold != nil && !hold.held {
		hold.mu.Lock()
		hold.held = true
	}
	unlockStep := lockWorktreeAddStep(repo)
	if hold != nil && hold.beforeAdd != nil {
		hold.beforeAdd()
	}
	return unlockStep
}

// endAddHold ends the hold of the create lock after the add and the tests
// that read the registrations folder after it.
func (h *createHold) endAddHold() {
	if h == nil || !h.held {
		return
	}
	h.release()
	afterAddHold()
}

// worktreeAddLocks is the lock of the add step per git directory. A create holds
// it while its `worktree add` runs (takeAddLocks). A remove holds it from the
// delete of the registration to its reply, and not for the delete of the leaf.
// 5fd08069 shows this order (measured on Linux and macOS): the leaf goes during
// a held add, and the registration and the branch go after it, then the reply. A
// create held at its read-tree checkout, after the add, does not delay a remove
// (measured on Linux). The key is that of the create lock (worktreeGitDirKey). A
// create takes no branch lock while it holds this one.
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
	return pathcanon.Canonical(key)
}

// worktreeBranchLocks serialises the branch step (worktreebranch.go) per common git
// dir. The lock of underRepoMutex is keyed by baseRepo. So it does not cover
// two removes through the repository and through one of its linked worktrees.
// 89cb6289 runs those two branch steps one after the other (measured on Linux). The
// key is the git dir under whose worktrees directory the entries are removed
// (verifyGitDir). That key is claustrum's choice (not measured): the rows do not
// tell the common dir from the main worktree. A create rollback takes the same lock
// (not measured). The branch lock is always taken last, so it does not deadlock with
// the per-repo lock.
var worktreeBranchLocks sync.Map // canonical common git dir -> *sync.Mutex

// lockWorktreeBranchStep takes the branch lock of repo and returns its unlock.
func lockWorktreeBranchStep(repo string) func() {
	m, _ := worktreeBranchLocks.LoadOrStore(worktreeGitDirKey(repo), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
