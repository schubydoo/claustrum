//go:build !windows

package git

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// holdDownload makes the download of the object of name wait until the
// returned release runs.
func (f lfsFixture) holdDownload(t *testing.T, name string) (release func()) {
	t.Helper()
	hold := filepath.Join(f.remote, f.oids[name]+".hold")
	mustWrite(t, hold, "")
	release = func() { _ = os.Remove(hold) }
	t.Cleanup(release)
	return release
}

// createLine is the line of a git.worktree_create of the branch branch.
func (f lfsFixture) createLine(t *testing.T, branch string) string {
	t.Helper()
	leaf := filepath.Join(f.repo, ".claude", "worktrees", branch)
	return rpcLine(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": branch, "worktreePath": leaf})
}

// TestWorktreeCreateRollbackWaitsForAnotherAdd: create A fails at its
// checkout while create B of the same repository, with a leaf of the same last
// name, is held in its `worktree add`. A checks out without the create lock,
// and its rollback takes the lock again first. So the rollback of A runs only
// after the add of B and the tests after it. B then gets a registration name
// of its own and succeeds, and A removes only its own registration
// (claustrum's own: the rollback deletes no name that B is making).
//
// Mutation: make createHold.retake a no-op. A then answers
// and removes its registration n1 during the add of B, git gives B the name
// n1 again, and B is refused with otherEntryRefusal.
func TestWorktreeCreateRollbackWaitsForAnotherAdd(t *testing.T) {
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	h := newTestGitDeps()
	leafA := filepath.Join(f.top, ".claude", "worktrees", "x", "n1")
	leafB := filepath.Join(f.top, ".claude", "worktrees", "y", "n1")
	gates := t.TempDir()
	gateA, gateB := filepath.Join(gates, "a"), filepath.Join(gates, "b")
	// The gates open at the end in every case, so no held call stays.
	t.Cleanup(func() {
		_ = os.WriteFile(gateA, nil, 0o644)
		_ = os.WriteFile(gateB, nil, 0o644)
	})
	// Rule 1: the read-tree call of A waits at gateA and then fails. Its
	// --work-tree names the leaf with the links of its path resolved
	// (checkoutWorkTree). On macOS the temp folder is below /var, a link to
	// /private/var.
	realTop, err := filepath.EvalSymlinks(f.top)
	if err != nil {
		t.Fatal(err)
	}
	workTreeA := filepath.Join(realTop, ".claude", "worktrees", "x", "n1")
	t.Setenv("CLAUSTRUM_GITSTUB_GATE", gateA)
	slowGit(t, "read-tree,--work-tree="+workTreeA, "gatefail", 60*time.Second, "", "")
	// Rule 2: the add of B waits at gateB and then runs git.
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "worktree,add,"+leafB)
	t.Setenv("CLAUSTRUM_GITSTUB_GATE2", gateB)
	t.Setenv("CLAUSTRUM_GITSTUB_MS2", "60000")
	var retakes atomic.Int32
	prev := createLockRetakes
	createLockRetakes = func() { retakes.Add(1) }
	t.Cleanup(func() { createLockRetakes = prev })
	a := dispatchAsync(h, rpcLine(t, "git.worktree_create", createParams(f.top, leafA, "ba")))
	waitFor(t, "the checkout of A reached its gate", func() bool { return exists(gateA + ".in") })
	b := dispatchAsync(h, rpcLine(t, "git.worktree_create", createParams(f.top, leafB, "bb")))
	waitFor(t, "the add of B reached its gate", func() bool { return exists(gateB + ".in") })
	if err := os.WriteFile(gateA, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// The rollback of A either waits for the lock (the sentinel) or answers.
	// Then B must still be held, with the registration of A in place.
	var rawA string
	if !pollUntil(60*time.Second, func() bool {
		if rawA == "" {
			select {
			case rawA = <-a:
			default:
			}
		}
		return retakes.Load() > 0 || rawA != ""
	}) {
		t.Fatal("the rollback of A neither waited for the lock nor answered in 60 s")
	}
	if rawA != "" {
		t.Errorf("create A answered while the add of B was held: %s", rawA)
	}
	select {
	case got := <-b:
		t.Fatalf("create B answered while its add was held: %s", got)
	default:
	}
	if regs := f.regs(t); len(regs) != 1 || regs[0] != "n1" {
		t.Errorf("registrations while B is held = %q, want only n1 of A", regs)
	}
	if err := os.WriteFile(gateB, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rawB := receive(t, "create B", b)
	if rawA == "" {
		rawA = receive(t, "create A", a)
	}
	if !strings.Contains(rawB, `"success":true`) {
		t.Errorf("create B = %s\nwant success", rawB)
	}
	if !strings.Contains(rawA, `"success":false`) || !strings.Contains(rawA, "git worktree add failed (checkout)") {
		t.Errorf("create A = %s\nwant the failed checkout", rawA)
	}
	if exists(leafA) {
		t.Error("the leaf of A stays after its rollback")
	}
	regs := f.regs(t)
	if len(regs) != 1 {
		t.Fatalf("registrations = %q, want only the one of B", regs)
	}
	gitdir, err := os.ReadFile(filepath.Join(f.regDir, regs[0], "gitdir"))
	if want := filepath.Join(realTop, ".claude", "worktrees", "y", "n1", ".git"); err != nil || strings.TrimSpace(string(gitdir)) != want {
		t.Errorf("the registration %s names %q, %v, want the leaf of B", regs[0], gitdir, err)
	}
	if branchExists(t, f.top, "ba") || !branchExists(t, f.top, "bb") {
		t.Error("want the branch bb and no branch ba")
	}
}

// TestLFSTwoCreatesCheckOutAtOnce: two creates of one repository check out at
// the same time. The first holds the download of b.bin, and the second create
// reaches its own smudge of b.bin during that hold. Both then download a.bin
// and b.bin, as on 4c111e86 with two creates started 0 s or 1 s apart
// (measured on Linux). The release before the checkout step is claustrum's
// own. Mutation: drop the call of release before runWorktreeCheckout in
// gitWorktreeCreateLocked (the second create waits for the first).
func TestLFSTwoCreatesCheckOutAtOnce(t *testing.T) {
	seamFreeSpace(t)
	// No tick keeps a.bin into the store of the repository during the hold.
	setKeepTick(t, time.Hour)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
	f.onServer(t, "a.bin")
	f.onServer(t, "b.bin")
	release := f.holdDownload(t, "b.bin")
	lines := func(name string) (n, downloads int) {
		b, _ := os.ReadFile(f.log)
		for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
			if short, rest, _ := strings.Cut(line, " "); short != "" && strings.HasPrefix(f.oids[name], short) {
				n++
				if strings.HasPrefix(rest, "present=false") {
					downloads++
				}
			}
		}
		return n, downloads
	}
	first := dispatchAsync(f.d, f.createLine(t, "w1"))
	waitFor(t, "the first create holds the download of b.bin", func() bool { n, _ := lines("b.bin"); return n == 1 })
	second := dispatchAsync(f.d, f.createLine(t, "w2"))
	if !pollUntil(20*time.Second, func() bool { n, _ := lines("b.bin"); return n == 2 }) {
		release()
		t.Fatal("the second create reached no checkout within 20 s while the first one held its download")
	}
	release()
	for _, ch := range []<-chan string{first, second} {
		if raw := receive(t, "a create", ch); !strings.Contains(raw, `"success":true`) {
			t.Errorf("git.worktree_create = %s, want success", raw)
		}
	}
	for _, name := range []string{"a.bin", "b.bin"} {
		if _, d := lines(name); d != 2 {
			t.Errorf("%s was downloaded %d times, want 2", name, d)
		}
	}
}
