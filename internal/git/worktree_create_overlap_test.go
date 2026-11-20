package git

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// overlapFixture holds the first `worktree add` of the shadow fixture at the
// gate of the stub. lockWaits counts the adds that reached their locks
// (beforeAddLocks). The first create that ends its hold of the create lock
// after its add then waits until open runs, at most 60 s (afterAddHold).
type overlapFixture struct {
	offHoldFixture
	lockWaits *atomic.Int32
	open      func()
}

func newOverlapFixture(t *testing.T) overlapFixture {
	t.Helper()
	h := newOffHoldFixture(t, "worktree,add")
	var waits atomic.Int32
	gate := make(chan struct{})
	var once sync.Once
	var ended atomic.Bool
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)
	oldBefore, oldAfter := beforeAddLocks, afterAddHold
	beforeAddLocks = func() { waits.Add(1) }
	// Only the first end waits. A sync.Once would make the later ends wait too.
	afterAddHold = func() {
		if ended.Swap(true) {
			return
		}
		select {
		case <-gate:
		case <-time.After(time.Minute):
		}
	}
	t.Cleanup(func() { beforeAddLocks, afterAddHold = oldBefore, oldAfter })
	return overlapFixture{offHoldFixture: h, lockWaits: &waits, open: open}
}

// atLocks waits up to 60 s until a second add reached its locks.
func (h overlapFixture) atLocks(t *testing.T) {
	t.Helper()
	for end := time.Now().Add(time.Minute); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if h.lockWaits.Load() >= 2 {
			return
		}
	}
	t.Fatal("C2 did not reach the locks of its add within 60 s")
}

// calls is the count of the git calls of the log that have words.
func (h overlapFixture) calls(t *testing.T, words ...string) int {
	t.Helper()
	n := 0
	for _, c := range readEnvCalls(t, h.log) {
		if argvHas(c.argv, words...) {
			n++
		}
	}
	return n
}

// create starts a git.worktree_create of the repository with the id n and the
// branch and leaf name.
func (h overlapFixture) create(t *testing.T, n int, name string) string {
	t.Helper()
	leaf := filepath.Join(h.repo, ".claude", "worktrees", name)
	h.send(t, n, "git.worktree_create", map[string]any{"baseRepo": h.repo, "branchName": name, "worktreePath": leaf})
	return leaf
}

// TestCreateRunsItsCallsDuringAnotherAdd: while the add of create C1 is held,
// create C2 of the same repository runs its git calls before its add, and then
// waits for the locks of the add. After the release both creates succeed.
// 4c111e86 runs those calls of C2 during the held add of C1, and both
// creates succeed (measured on Linux, in its call log).
//
// Mutation: take the create lock at the start of withWorktreeCreateLock, as
// before TASK-705. C2 then makes no git call while C1 is held.
func TestCreateRunsItsCallsDuringAnotherAdd(t *testing.T) {
	h := newOverlapFixture(t)
	h.open()
	leaf1 := h.create(t, 1, "c1")
	h.held(t)
	before := h.calls(t, "rev-parse", "--absolute-git-dir")
	leaf2 := h.create(t, 2, "c2")
	h.atLocks(t)
	if got := h.calls(t, "rev-parse", "--absolute-git-dir"); got <= before {
		t.Errorf("C2 made %d rev-parse --absolute-git-dir calls during the held add, want at least 1", got-before)
	}
	if got := h.calls(t, "worktree", "add"); got != 1 {
		t.Errorf("%d worktree add calls during the held add, want 1", got)
	}
	h.release()
	for n, leaf := range map[int]string{1: leaf1, 2: leaf2} {
		want := `{"success":true,"path":"` + jsonInner(t, leaf) + `","sourceBranch":"main","branch":"` + filepath.Base(leaf) + `"}`
		if got := string(h.reply(t, n)); got != want {
			t.Errorf("create %d = %s\nwant %s", n, got, want)
		}
	}
}

// TestCreateFailsWhenItsCopyWentDuringAnotherAdd: C1 is held in its
// add, C2 waits for the locks of its add, and a git.info on a new repository B
// at the full limit removes the folder of the copy of the repository. After
// the release C2 answers privateCopyText and the error of the stat of that
// folder, with errorCode worktree_add_failed, and makes no add. C1 succeeds,
// and its next calls make the folder again. 4c111e86 gives C2 that text
// (measured on Linux, macOS and Windows), and C1 succeeds (measured on Linux).
// The remake is claustrum's own. C1 waits after the end of its hold for the reply
// of C2 (afterAddHold), so the test does not race the next call of C1.
//
// Mutations: take the create lock at the start of withWorktreeCreateLock (C2
// then waits before its calls, and its listing makes the folder again), or
// hold the folder of the copy in runAddStaged (B then gets the limit text).
func TestCreateFailsWhenItsCopyWentDuringAnotherAdd(t *testing.T) {
	lowerRepoLimit(t, 1)
	h := newOverlapFixture(t)
	leaf1 := h.create(t, 1, "c1")
	h.held(t)
	leaf2 := h.create(t, 2, "c2")
	h.atLocks(t)
	h.send(t, 3, "git.info", map[string]any{"path": h.repoB})
	if raw := h.g.reply(t, 3); !strings.Contains(raw, `"isRepo":true`) {
		t.Errorf("git.info on B during the add = %s\nwant a result with isRepo true", raw)
	}
	folderX := filepath.Join(daemonFolder(t, h.run), shadowRepoName(filepath.Join(h.repo, ".git")))
	sweptGone(t, folderX)
	h.release()
	want2 := `{"success":false,"error":"` + jsonInner(t, privateCopyText+statGoneText()) + `","errorCode":"worktree_add_failed"}`
	if got := string(h.reply(t, 2)); got != want2 {
		t.Errorf("create C2 = %s\nwant %s", got, want2)
	}
	h.open()
	want1 := `{"success":true,"path":"` + jsonInner(t, leaf1) + `","sourceBranch":"main","branch":"c1"}`
	if got := string(h.reply(t, 1)); got != want1 {
		t.Errorf("create C1 = %s\nwant %s", got, want1)
	}
	if got := h.calls(t, "worktree", "add"); got != 1 {
		t.Errorf("%d worktree add calls, want 1: C2 makes none", got)
	}
	sweptGone(t, leaf2)
	if branchThere(t, h.repo, "c2") {
		t.Error("the branch c2 stays")
	}
	sweptThere(t, folderX)
}
