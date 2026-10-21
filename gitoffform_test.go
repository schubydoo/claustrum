package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The off switch at each git command of git.status, git.worktree_create and
// git.worktree_remove (cells of round task105-01, Linux VM). Request A runs on
// the repository of the fixture without the member. Request B is a
// git.list_branches on repository B with "shadowDirOff":true, and the second
// rule of the stub holds its for-each-ref. While B is held the hold is live.

// holdB makes the second rule of the stub hold the for-each-ref of B.
func holdB(t *testing.T, h liveFixture) {
	t.Helper()
	holdSecond(t, "for-each-ref,--sort=refname", h.repoB, false)
}

// sendB sends request B with the id n.
func (h liveFixture) sendB(t *testing.T, n int) {
	t.Helper()
	h.send(t, n, "git.list_branches", map[string]any{"path": h.repoB, "shadowDirOff": true})
}

// warm answers one git.info on A, so the start test passed and A has a copy.
func (h liveFixture) warm(t *testing.T) {
	t.Helper()
	h.send(t, 90, "git.info", map[string]any{"path": h.repo})
	h.reply(t, 90)
}

// holdA makes the first rule of the stub hold the first call of A whose argv
// holds the words of match.
func holdA(t *testing.T, match string) {
	t.Helper()
	slowGit(t, match, "gate", 10*time.Minute, "", "")
}

// startsInA: A runs until the stub holds its call, then B starts and is held,
// then A goes on under the hold and answers. B answers after A.
func (h liveFixture) startsInA(t *testing.T, method string, params map[string]any) string {
	t.Helper()
	holdB(t, h)
	h.send(t, 1, method, params)
	h.waitIn(t, h.gate1)
	h.sendB(t, 2)
	h.waitIn(t, h.gate2)
	h.open(h.gate1)
	got := liveResult(t, h.reply(t, 1))
	h.open(h.gate2)
	h.reply(t, 2)
	return got
}

// endsInA: B starts and is held, then A starts under the hold until the stub
// holds its call. B then answers, so the hold ends, and A goes on.
func (h liveFixture) endsInA(t *testing.T, method string, params map[string]any) string {
	t.Helper()
	holdB(t, h)
	h.sendB(t, 2)
	h.waitIn(t, h.gate2)
	h.send(t, 1, method, params)
	h.waitIn(t, h.gate1)
	h.open(h.gate2)
	h.reply(t, 2)
	h.open(h.gate1)
	return liveResult(t, h.reply(t, 1))
}

// callAt is the index of the first call of A from the index from on whose
// argv holds the words of match, or -1. A call in repository B is a call of B.
func (h liveFixture) callAt(calls []envCall, from int, match string) int {
	for i := from; i < len(calls); i++ {
		if stubMatches(calls[i].argv, match) && !sameDir(calls[i].cwd, h.repoB) {
			return i
		}
	}
	return -1
}

// callCommonDir is the GIT_COMMON_DIR of a call, or "".
func callCommonDir(c envCall) string {
	v, _ := lookupExact(c.env, "GIT_COMMON_DIR")
	return v
}

// wantForm tests the GIT_COMMON_DIR of the first call from the index from on
// with the words of match: the copy with copy set, else the git directory of
// A, by identity. It returns the index of the call.
func (h liveFixture) wantForm(t *testing.T, calls []envCall, from int, match string, copy bool) int {
	t.Helper()
	i := h.callAt(calls, from, match)
	if i < 0 {
		t.Errorf("no call %q after call %d", match, from)
		return from
	}
	v := callCommonDir(calls[i])
	switch {
	case copy && !namesCopy(v):
		t.Errorf("call %d %q has GIT_COMMON_DIR=%q, want the copy", i, match, v)
	case !copy && !sameDir(v, h.gitDir):
		t.Errorf("call %d %q has GIT_COMMON_DIR=%q, want the git directory %s", i, match, v, h.gitDir)
	}
	return i
}

// probeAfter reports whether a call that asks git about a repository runs
// between the indexes from and to.
func probeAfter(calls []envCall, from, to int) bool {
	for i := from; i < to && i < len(calls); i++ {
		if filepath.Base(callCommonDir(calls[i])) == ".probe" {
			return true
		}
	}
	return false
}

// leafListings counts the listings in the folder leaf between the indexes
// from and to.
func leafListings(calls []envCall, leaf string, from, to int) int {
	n := 0
	for i := from; i < to; i++ {
		if calls[i].listing() && sameDir(calls[i].cwd, leaf) {
			n++
		}
	}
	return n
}

// wantIndexIn tests the GIT_INDEX_FILE of the read-tree call c. With inCopy
// set it is in `.index-<n>` of the folder of the daemon, else in a folder
// `claude-ssh-index-<n>` outside the run folder.
func wantIndexIn(t *testing.T, c envCall, inCopy bool) {
	t.Helper()
	v, _ := lookupExact(c.env, "GIT_INDEX_FILE")
	dir := filepath.Base(filepath.Dir(v))
	switch {
	case inCopy && (!strings.HasPrefix(dir, shadowIndexDirPrefix) || !strings.Contains(v, shadowRootName)):
		t.Errorf("read-tree has GIT_INDEX_FILE=%q, want it in %s<n> of the folder of the daemon", v, shadowIndexDirPrefix)
	case !inCopy && (!strings.HasPrefix(dir, checkoutIndexTempPrefix) || strings.Contains(v, shadowRootName)):
		t.Errorf("read-tree has GIT_INDEX_FILE=%q, want it in %s<n> of the temporary folder", v, checkoutIndexTempPrefix)
	}
}

// wantCopyLink tests that the folder of the daemon holds a copy with its
// link `worktrees` (Linux and macOS: Windows has no such link).
func (h liveFixture) wantCopyLink(t *testing.T) {
	t.Helper()
	if !shadowLinksWorktrees {
		return
	}
	daemon := daemonFolder(t, h.s.gitShadow.runDir)
	for _, name := range entryNames(t, daemon) {
		if fi, err := os.Lstat(filepath.Join(daemon, name, worktreesSubdir)); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return
		}
	}
	t.Errorf("the folder of the daemon %s holds no copy with a link %s", daemon, worktreesSubdir)
}

// createParams is the create of the cells: the new worktree w1 on the branch w1.
func (h liveFixture) createParams() (map[string]any, string) {
	leaf := filepath.Join(h.repo, ".claude", "worktrees", "w1")
	return map[string]any{"baseRepo": h.repo, "branchName": "w1", "worktreePath": leaf}, leaf
}

func (h liveFixture) createdText(t *testing.T, leaf string) string {
	return `{"success":true,"path":` + jsonQuoted(t, leaf) + `,"sourceBranch":"main","branch":"w1"}`
}

// S1: a hold that starts while the `ls-files` of a git.status runs. Every later
// command uses the git directory itself: the config listings, hash-object,
// `rev-parse --verify` and `diff-index` (cell S1 of round task105-01, Linux VM).
// Mutation: drop the call of req.keepLiveForm in gitStatus. The later commands
// then name the copy.
func TestShadowStatusFollowsAHoldThatStarts(t *testing.T) {
	h := newLiveFixture(t)
	h.warm(t)
	holdA(t, "ls-files,-s,-z")
	if got := h.startsInA(t, "git.status", map[string]any{"path": h.wt, "baseRepo": h.repo}); got != `{"isRepo":true,"clean":true}` {
		t.Errorf("S1: git.status = %s, want clean", got)
	}
	calls := readEnvCalls(t, h.log)
	i := h.wantForm(t, calls, 0, "ls-files,-s,-z", true)
	for _, m := range []string{"hash-object", "rev-parse,--verify,-q,HEAD^{commit}", "diff-index,--cached"} {
		h.wantForm(t, calls, i+1, m, false)
	}
}

// S2: a hold that ends while the `status` call of a git.status runs, on a
// daemon with no start test. The next command runs the start test with no
// call that asks git about the repository, and the later commands name the
// copy. The copy has its link `worktrees` after the reply (cell S2 of round
// task105-01, Linux VM). Mutation: drop the call of req.keepLiveForm in
// gitStatus. Every command then keeps the git directory and no test runs. A
// second mutation calls settingsGate with ask true in keepLiveForm: a call
// about the repository then follows the test.
func TestShadowStatusFollowsAHoldThatEnds(t *testing.T) {
	h := newLiveFixture(t)
	holdA(t, "status,--porcelain")
	if got := h.endsInA(t, "git.status", map[string]any{"path": h.wt, "baseRepo": h.repo}); got != `{"isRepo":true,"clean":true}` {
		t.Errorf("S2: git.status = %s, want clean", got)
	}
	calls := readEnvCalls(t, h.log)
	i := h.wantForm(t, calls, 0, "status,--porcelain", false)
	test := h.callAt(calls, i+1, "init,-q,--template=")
	if test < 0 {
		t.Fatal("S2: no start test after the held status call")
	}
	j := h.wantForm(t, calls, test, "ls-files,-s,-z", true)
	if probeAfter(calls, test, j) {
		t.Error("S2: a call asks git about the repository after the start test, want none")
	}
	for _, m := range []string{"rev-parse,--verify,-q,HEAD^{commit}", "diff-index,--cached"} {
		h.wantForm(t, calls, j+1, m, true)
	}
	h.wantCopyLink(t)
}

// C1: a hold that starts while `rev-parse --absolute-git-dir` of a create runs.
// The add and the read-tree use the git directory itself, the index is in
// `claude-ssh-index-<n>` of the temporary folder, and one listing comes
// before the read-tree (cell C1 of round task105-01, Linux VM). Mutation: drop
// the call of req.keepLiveForm in gitWorktreeCreateLocked. Every call then
// names the copy, with two listings and the index in `.index-<n>`. A second
// mutation gives the add the use u in place of u.fixedForm() in worktreeAdd:
// the add then keeps the copy.
func TestShadowCreateFollowsAHoldThatStartsBeforeTheAdd(t *testing.T) {
	h := newLiveFixture(t)
	h.warm(t)
	holdA(t, "rev-parse,--absolute-git-dir")
	params, leaf := h.createParams()
	if got, want := h.startsInA(t, "git.worktree_create", params), h.createdText(t, leaf); got != want {
		t.Errorf("C1: create = %s\n  want %s", got, want)
	}
	calls := readEnvCalls(t, h.log)
	i := h.wantForm(t, calls, 0, "rev-parse,--absolute-git-dir", true)
	add := h.wantForm(t, calls, i+1, "worktree,add", false)
	rt := h.wantForm(t, calls, add+1, "read-tree", false)
	wantIndexIn(t, calls[rt], false)
	if n := leafListings(calls, leaf, add+1, rt); n != 1 {
		t.Errorf("C1: %d listings before the read-tree, want 1", n)
	}
}

// C2: a hold that starts while the `worktree add` of a create is held before
// git runs. The add keeps the copy, and the read-tree uses the git directory
// itself with the index in the temporary folder and one listing (cell C2 of
// round task105-01, Linux VM). Mutation: in gitWorktreeCreateLocked, give the
// checkout req.use in place of req.use.fixedForm(). The read-tree then names
// the copy, with two listings.
func TestShadowCreateFollowsAHoldThatStartsAtTheAdd(t *testing.T) {
	h := newLiveFixture(t)
	h.warm(t)
	holdA(t, "worktree,add")
	params, leaf := h.createParams()
	if got, want := h.startsInA(t, "git.worktree_create", params), h.createdText(t, leaf); got != want {
		t.Errorf("C2: create = %s\n  want %s", got, want)
	}
	calls := readEnvCalls(t, h.log)
	add := h.wantForm(t, calls, 0, "worktree,add", true)
	rt := h.wantForm(t, calls, add+1, "read-tree", false)
	wantIndexIn(t, calls[rt], false)
	if n := leafListings(calls, leaf, add+1, rt); n != 1 {
		t.Errorf("C2: %d listings before the read-tree, want 1", n)
	}
}

// C3: a hold that ends while the `worktree add` of a create is held, on a
// daemon with no start test. The add uses the git directory itself. Then the
// start test runs with no call that asks git about the repository, and the
// read-tree names the copy, with the index in `.index-<n>` and two listings.
// The copy has its link after the reply (cell C3 of round task105-01, Linux
// VM). Mutation: drop the call of req.keepLiveForm in gitWorktreeCreateLocked.
// The read-tree then keeps the git directory, with no start test.
func TestShadowCreateFollowsAHoldThatEndsAtTheAdd(t *testing.T) {
	h := newLiveFixture(t)
	holdA(t, "worktree,add")
	params, leaf := h.createParams()
	if got, want := h.endsInA(t, "git.worktree_create", params), h.createdText(t, leaf); got != want {
		t.Errorf("C3: create = %s\n  want %s", got, want)
	}
	calls := readEnvCalls(t, h.log)
	add := h.wantForm(t, calls, 0, "worktree,add", false)
	test := h.callAt(calls, add+1, "init,-q,--template=")
	if test < 0 {
		t.Fatal("C3: no start test after the add")
	}
	rt := h.wantForm(t, calls, test, "read-tree", true)
	wantIndexIn(t, calls[rt], true)
	if n := leafListings(calls, leaf, test, rt); n != 2 {
		t.Errorf("C3: %d listings before the read-tree, want 2", n)
	}
	if probeAfter(calls, test, rt) {
		t.Error("C3: a call asks git about the repository after the start test, want none")
	}
	h.wantCopyLink(t)
}

// C0: a create with no hold. Every call with a GIT_COMMON_DIR names the copy
// or the folder .probe, the index is in `.index-<n>`, and two listings come
// before the read-tree. Both builds do so (cell C0 of round task105-01, Linux
// VM). This row is the control: it passes on the build before the change too.
func TestShadowCreateWithNoHoldNamesTheCopy(t *testing.T) {
	h := newLiveFixture(t)
	h.warm(t)
	if err := os.WriteFile(h.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	params, leaf := h.createParams()
	h.send(t, 1, "git.worktree_create", params)
	if got, want := liveResult(t, h.reply(t, 1)), h.createdText(t, leaf); got != want {
		t.Errorf("C0: create = %s\n  want %s", got, want)
	}
	calls := readEnvCalls(t, h.log)
	for i, c := range calls {
		if v := callCommonDir(c); v != "" && !strings.Contains(v, shadowRootName) {
			t.Errorf("C0: call %d %q has GIT_COMMON_DIR=%q, want the copy", i, strings.Join(c.argv, " "), v)
		}
	}
	add := h.wantForm(t, calls, 0, "worktree,add", true)
	rt := h.wantForm(t, calls, add+1, "read-tree", true)
	wantIndexIn(t, calls[rt], true)
	if n := leafListings(calls, leaf, add+1, rt); n != 2 {
		t.Errorf("C0: %d listings before the read-tree, want 2", n)
	}
}

// removeParams is the remove of the cells: the linked worktree of the fixture
// and its branch wr.
func (h liveFixture) removeParams() map[string]any {
	return map[string]any{"baseRepo": h.repo, "worktreePath": h.wt, "branchName": "wr"}
}

// wantRemoved tests that the worktree of the fixture and its branch are gone.
func (h liveFixture) wantRemoved(t *testing.T, cell string) {
	t.Helper()
	if _, err := os.Lstat(h.wt); !os.IsNotExist(err) {
		t.Errorf("%s: the worktree folder is still there (%v)", cell, err)
	}
	if branchExists(t, h.repo, "wr") {
		t.Errorf("%s: the branch wr is still there", cell)
	}
}

// R1: a hold that starts while the for-each-ref of the branch step of a remove
// runs. rev-list and update-ref use the git directory itself, and the branch
// goes (cell R1 of round task105-01, Linux VM). Mutation: drop the call of
// req.keepLiveForm in removeGate. rev-list and update-ref then name the copy.
// Two more mutations drop the u.atCommand() of branchCheckGit, or that of
// deleteBranchRef.
func TestShadowRemoveFollowsAHoldThatStarts(t *testing.T) {
	h := newLiveFixture(t)
	h.warm(t)
	holdA(t, "for-each-ref,refs/heads/")
	if got := h.startsInA(t, "git.worktree_remove", h.removeParams()); got != `{"success":true}` {
		t.Errorf("R1: remove = %s, want success", got)
	}
	calls := readEnvCalls(t, h.log)
	i := h.wantForm(t, calls, 0, "for-each-ref,refs/heads/", true)
	h.wantForm(t, calls, i+1, "rev-list", false)
	h.wantForm(t, calls, i+1, "update-ref,--no-deref,-d", false)
	h.wantRemoved(t, "R1")
}

// R2: a hold that ends while the `rev-parse --absolute-git-dir` of a remove
// runs, on a daemon with no start test. The next command runs the start test,
// then a call asks git about the repository, then for-each-ref, rev-list and
// update-ref name the copy. The worktree and its branch go (cell R2 of round
// task105-01, Linux VM). The call about the repository comes before the
// daemon's own read of the config (cell R2b of round task105-04, Linux VM).
// Mutation: drop the call of req.keepLiveForm in removeGate. Every call then
// keeps the git directory, with no start test. A second mutation gives
// settingsGate ask false in keepLiveForm: the read of the config then comes
// first.
func TestShadowRemoveFollowsAHoldThatEnds(t *testing.T) {
	h := newLiveFixture(t)
	holdA(t, "rev-parse,--absolute-git-dir")
	if got := h.endsInA(t, "git.worktree_remove", h.removeParams()); got != `{"success":true}` {
		t.Errorf("R2: remove = %s, want success", got)
	}
	calls := readEnvCalls(t, h.log)
	i := h.wantForm(t, calls, 0, "rev-parse,--absolute-git-dir", false)
	test := h.callAt(calls, i+1, "init,-q,--template=")
	if test < 0 {
		t.Fatal("R2: no start test after the held call")
	}
	fer := h.wantForm(t, calls, test, "for-each-ref,refs/heads/", true)
	h.wantProbeFirst(t, "R2", calls, test, fer)
	h.wantForm(t, calls, fer+1, "rev-list", true)
	h.wantForm(t, calls, fer+1, "update-ref,--no-deref,-d", true)
	h.wantRemoved(t, "R2")
}

// wantProbeFirst tests that a call about the repository runs after the start
// test that begins at the index test and before the index to. It comes before
// the first read of the config there (envCall.configRead). 5fd08069 has that
// order after a later start test (cells R2b and C5 of round task105-04, Linux
// VM). The last call of the test is its own `worktree add --detach`.
func (h liveFixture) wantProbeFirst(t *testing.T, cell string, calls []envCall, test, to int) {
	t.Helper()
	end := h.callAt(calls, test, "worktree,add,--detach")
	if end < 0 || end > to {
		t.Fatalf("%s: the start test has no end before call %d", cell, to)
	}
	probe, read := -1, -1
	for i := end + 1; i < to && i < len(calls); i++ {
		if probe < 0 && filepath.Base(callCommonDir(calls[i])) == ".probe" {
			probe = i
		}
		if read < 0 && calls[i].configRead() {
			read = i
		}
	}
	switch {
	case probe < 0:
		t.Errorf("%s: no call asks git about the repository after the start test", cell)
	case read >= 0 && read < probe:
		t.Errorf("%s: the read of the config (call %d) comes before the call about the repository (call %d)", cell, read, probe)
	}
}

// dropFixtureWorktree removes the linked worktree of the fixture and its
// registrations folder, as in the repository of cells C4 and C5: no pair of
// calls then runs before the add (pairBeforeAdd).
func (h liveFixture) dropFixtureWorktree(t *testing.T) {
	t.Helper()
	buildGit(t, h.repo, "worktree", "remove", "--force", h.wt)
	if err := os.RemoveAll(filepath.Join(h.gitDir, worktreesSubdir)); err != nil {
		t.Fatal(err)
	}
}

// wantLeafRepointed tests that the `.git` file of the new worktree names no
// folder of the daemon after the reply (repointLeaf).
func wantLeafRepointed(t *testing.T, cell, leaf string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(leaf, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), shadowRootName) {
		t.Errorf("%s: the .git file of the leaf is %q, want it to name the git directory", cell, b)
	}
}

// C4: a create whose gate ran under the hold, on a daemon with no start test.
// The hold ends while `rev-parse --absolute-git-dir` runs. The next command runs
// the start test, then a call about the repository, and the add and every later
// command name the copy (cell C4 of round task105-04, Linux VM). The create
// holds its git directory from that gate on, so the `.git` file of the leaf is
// replaced as after a copy gate. Mutation: in worktreeAdd, take the form only
// when u.accepted is set (the old choice). The add then keeps the git
// directory.
func TestShadowCreateFollowsAHoldThatEndsBeforeTheAdd(t *testing.T) {
	h := newLiveFixture(t)
	h.dropFixtureWorktree(t)
	holdA(t, "rev-parse,--absolute-git-dir")
	params, leaf := h.createParams()
	if got, want := h.endsInA(t, "git.worktree_create", params), h.createdText(t, leaf); got != want {
		t.Errorf("C4: create = %s\n  want %s", got, want)
	}
	calls := readEnvCalls(t, h.log)
	i := h.wantForm(t, calls, 0, "rev-parse,--absolute-git-dir", false)
	test := h.callAt(calls, i+1, "init,-q,--template=")
	if test < 0 {
		t.Fatal("C4: no start test after the held call")
	}
	add := h.wantForm(t, calls, test, "worktree,add,--no-track", true)
	h.wantProbeFirst(t, "C4", calls, test, add)
	rt := h.wantForm(t, calls, add+1, "read-tree", true)
	wantIndexIn(t, calls[rt], true)
	wantLeafRepointed(t, "C4", leaf)
	h.wantCopyLink(t)
}

// C5: as C4, with the hold that ends while `rev-parse --is-inside-work-tree`
// runs. After the start test and the call about the repository, the abbrev-ref
// call, `rev-parse --absolute-git-dir`, the add and the read-tree name the copy
// (cell C5 of round task105-04, Linux VM). Mutation: in keepLiveCreate, leave
// keep false, so the create does not adopt the later gate. The add then names
// the copy with no held git directory, and the `.git` file of the leaf keeps
// the path of the copy (Linux). A second mutation gives settingsGate ask false
// there: the read of the config then comes first.
func TestShadowCreateFollowsAHoldThatEndsAtTheRepositoryTest(t *testing.T) {
	h := newLiveFixture(t)
	h.dropFixtureWorktree(t)
	holdA(t, "rev-parse,--is-inside-work-tree")
	params, leaf := h.createParams()
	if got, want := h.endsInA(t, "git.worktree_create", params), h.createdText(t, leaf); got != want {
		t.Errorf("C5: create = %s\n  want %s", got, want)
	}
	calls := readEnvCalls(t, h.log)
	i := h.wantForm(t, calls, 0, "rev-parse,--is-inside-work-tree", false)
	test := h.callAt(calls, i+1, "init,-q,--template=")
	if test < 0 {
		t.Fatal("C5: no start test after the held call")
	}
	ab := h.wantForm(t, calls, test, "rev-parse,--abbrev-ref,HEAD", true)
	h.wantProbeFirst(t, "C5", calls, test, ab)
	gd := h.wantForm(t, calls, ab+1, "rev-parse,--absolute-git-dir", true)
	add := h.wantForm(t, calls, gd+1, "worktree,add,--no-track", true)
	rt := h.wantForm(t, calls, add+1, "read-tree", true)
	wantIndexIn(t, calls[rt], true)
	wantLeafRepointed(t, "C5", leaf)
	h.wantCopyLink(t)
}
