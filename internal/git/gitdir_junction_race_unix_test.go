//go:build unix

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
)

// movedJunction is a repository reached through a symbolic link that stands for the
// junction S/P -> jtA, and a second repository in jtB.
type movedJunction struct {
	P, A, B string
}

// newMovedJunction builds the fixture. The link is made by the stand-in of the
// Windows walk.
func newMovedJunction(t *testing.T) movedJunction {
	t.Helper()
	base := resolveTestRoot(t, t.TempDir())
	f := movedJunction{P: filepath.Join(base, "S", "P"), A: filepath.Join(base, "jtA", "repo"), B: filepath.Join(base, "jtB", "repo")}
	initTrustMain(t, f.A)
	initTrustMain(t, f.B)
	if err := os.MkdirAll(filepath.Dir(f.P), 0o755); err != nil {
		t.Fatal(err)
	}
	junctionsLikeWindows(t)(t, f.P, filepath.Dir(f.A))
	junctionWalkLikeWindows(t, f.P)
	oldByHandle := leafResolvesByHandle
	t.Cleanup(func() { leafResolvesByHandle = oldByHandle })
	leafResolvesByHandle = true
	return f
}

// moveSteps are the steps that move the junction to the second repository.
func (f movedJunction) moveSteps() [][]string {
	return [][]string{{"rename", f.P, f.P + "old"}, {"symlink", filepath.Dir(f.B), f.P}}
}

// A junction moved to a second repository during a remove, right after `rev-parse
// --absolute-git-dir`. The leaf and the entry of the first repository go, its branch
// stays, and in the second repository only the ref of the same name goes (4c111e86
// and 87375cc7, measured on Windows). Mutations: in gitWorktreeRemoveLocked, drop the
// removePin test (the entry of B goes). Return before the branch step when
// junctionMoved (the ref of B stays). With "shadow": drop the removeGate call of
// that step (the ref of A goes).
func TestRemoveWithAMovedJunction(t *testing.T) {
	f := newMovedJunction(t)
	for _, repo := range []string{f.A, f.B} {
		runGit(t, repo, "worktree", "add", "-q", "-b", "w1", filepath.Join(repo, ".claude", "worktrees", "w1"))
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	installGitSlowStub(t, realGit)
	stubStepsNow(t, "rev-parse,--absolute-git-dir", f.moveSteps()...)
	spelled := filepath.Join(f.P, "repo")
	got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": spelled, "worktreePath": filepath.Join(spelled, ".claude", "worktrees", "w1"), "branchName": "w1"}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	f.wantRemovedFromFirst(t)
}

// wantRemovedFromFirst checks the end state of the row.
func (f movedJunction) wantRemovedFromFirst(t *testing.T) {
	t.Helper()
	if exists(filepath.Join(f.A, ".claude", "worktrees", "w1")) || exists(filepath.Join(f.A, ".git", "worktrees", "w1")) {
		t.Error("the leaf or the entry of the first repository stayed")
	}
	if out := gitOut(t, f.A, "branch", "--list", "w1"); !strings.Contains(out, "w1") {
		t.Errorf("the branch of the first repository went: %q", out)
	}
	for _, p := range []string{filepath.Join(f.B, ".claude", "worktrees", "w1", ".git"), filepath.Join(f.B, ".git", "worktrees", "w1", "HEAD")} {
		if !exists(p) {
			t.Errorf("%s of the second repository went", p)
		}
	}
	if out := gitOut(t, f.B, "for-each-ref", "refs/heads/w1"); out != "" {
		t.Errorf("the ref of the second repository stayed: %q", out)
	}
}

// stubStepsNow makes the git stub run steps right after the first call with the words
// of match.
func stubStepsNow(t *testing.T, match string, steps ...[]string) {
	t.Helper()
	joined := make([]string, len(steps))
	for i, s := range steps {
		joined[i] = strings.Join(s, "\x1f")
	}
	for k, v := range map[string]string{
		"CLAUSTRUM_GITSTUB_MATCH": match, "CLAUSTRUM_GITSTUB_MODE": "post", "CLAUSTRUM_GITSTUB_MS": "0",
		"CLAUSTRUM_GITSTUB_ACTION": "steps", "CLAUSTRUM_GITSTUB_VALUE": strings.Join(joined, "\x1e"),
		"CLAUSTRUM_GITSTUB_NTHFILE": filepath.Join(t.TempDir(), "nth"), "CLAUSTRUM_GITSTUB_NTH": "1",
	} {
		t.Setenv(k, v)
	}
}

// newShadowMovedJunction is a seeded shadow fixture whose repository sits behind the
// stand-in junction S/P -> jtA, with a second repository in jtB.
func newShadowMovedJunction(t *testing.T) (shadowFixture, movedJunction) {
	t.Helper()
	f := newSeededShadowFixture(t)
	root := filepath.Dir(f.repo)
	m := movedJunction{P: filepath.Join(root, "S", "P"), A: filepath.Join(root, "jtA", "repo"), B: filepath.Join(root, "jtB", "repo")}
	for _, d := range []string{filepath.Dir(m.A), filepath.Dir(m.B), filepath.Dir(m.P)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(f.repo, m.A); err != nil {
		t.Fatal(err)
	}
	initTrustMain(t, m.B)
	junctionsLikeWindows(t)(t, m.P, filepath.Dir(m.A))
	junctionWalkLikeWindows(t, m.P)
	old := leafResolvesByHandle
	t.Cleanup(func() { leafResolvesByHandle = old })
	leafResolvesByHandle = true
	return f, m
}

// The same race in the state shadow, where each call names the copy of the first
// repository until the branch step takes the gate of the second. Mutation: in
// gitWorktreeRemoveLocked, drop the removeGate call of the moved branch step (the ref
// of the first repository goes).
func TestRemoveWithAMovedJunctionInShadow(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	for _, repo := range []string{m.A, m.B} {
		runGit(t, repo, "worktree", "add", "-q", "-b", "w1", filepath.Join(repo, ".claude", "worktrees", "w1"))
	}
	stubStepsNow(t, "rev-parse,--absolute-git-dir", m.moveSteps()...)
	spelled := filepath.Join(m.P, "repo")
	raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": spelled,
		"worktreePath": filepath.Join(spelled, ".claude", "worktrees", "w1"), "branchName": "w1"})
	if !strings.Contains(raw, `"success":true`) {
		t.Errorf("git.worktree_remove = %s", raw)
	}
	m.wantRemovedFromFirst(t)
}

// A junction moved to a second repository right after `rev-parse --absolute-git-dir`
// of a create. The branch lands in the second repository. The registration and the
// `.git` file of the leaf land in the first, the reply is unsafe_path, and no rollback
// runs (4c111e86 and 87375cc7, measured on Windows). The second repository gets no
// registration and no leaf. Mutation: make noteJunctionMove answer false (the create
// stops before the add and no branch lands).
func TestCreateFollowsAMovedJunction(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	spelled := filepath.Join(m.P, "repo")
	if raw, _ := f.info(t, spelled, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	setStagedAddWindowsSteps(t, true)
	atStep(t, "answered", func() {
		for _, s := range m.moveSteps() {
			var err error
			if s[0] == "rename" {
				err = os.Rename(s[1], s[2])
			} else {
				err = os.Symlink(s[1], s[2])
			}
			if err != nil {
				t.Error(err)
			}
		}
	})
	leaf := filepath.Join(spelled, ".claude", "worktrees", "w2")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": spelled, "branchName": "w2", "worktreePath": leaf})
	want := "refusing to create worktree: " + tickPath(leaf) + " no longer leads to the directory that was created for the worktree"
	if text, code := createErrorField(t, raw); text != want || code != "unsafe_path" {
		t.Errorf("git.worktree_create = %s\nwant the error %s", raw, want)
	}
	if out := gitOut(t, m.B, "for-each-ref", "refs/heads/w2"); out == "" {
		t.Error("the second repository has no branch w2")
	}
	if out := gitOut(t, m.A, "for-each-ref", "refs/heads/w2"); out != "" {
		t.Errorf("the first repository has the branch: %q", out)
	}
	if !exists(filepath.Join(m.A, ".git", "worktrees", "w2", "gitdir")) || !exists(filepath.Join(m.A, ".claude", "worktrees", "w2", ".git")) {
		t.Error("the first repository lost the registration or the leaf")
	}
	if exists(filepath.Join(m.B, ".git", "worktrees", "w2")) || exists(filepath.Join(m.B, ".claude", "worktrees", "w2")) {
		t.Error("the second repository has the registration or the leaf")
	}
}

// A second repository whose gate refuses keeps its ref. Its `info/exclude` is a
// folder, which the gate refuses and git reads as no file. Mutation: in
// gitWorktreeRemoveLocked, drop the removeGate call of the moved branch step.
func TestRemoveWithAMovedJunctionKeepsARefusedRef(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	for _, repo := range []string{m.A, m.B} {
		runGit(t, repo, "worktree", "add", "-q", "-b", "w1", filepath.Join(repo, ".claude", "worktrees", "w1"))
	}
	exclude := filepath.Join(m.B, ".git", "info", "exclude")
	if err := os.RemoveAll(exclude); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(exclude, 0o755); err != nil {
		t.Fatal(err)
	}
	stubStepsNow(t, "rev-parse,--absolute-git-dir", m.moveSteps()...)
	spelled := filepath.Join(m.P, "repo")
	raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": spelled,
		"worktreePath": filepath.Join(spelled, ".claude", "worktrees", "w1"), "branchName": "w1"})
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`; raw != want {
		t.Errorf("git.worktree_remove = %s\nwant %s", raw, want)
	}
	if out := gitOut(t, m.B, "for-each-ref", "refs/heads/w1"); out == "" {
		t.Error("the ref of the refused second repository went")
	}
}

// A failed staging step after the add of a moved junction runs no rollback, so the
// new branch of the second repository stays. A `.git` planted in the leaf makes the
// step fail. Mutation: in incompleteAdd, drop the junctionMoved return.
func TestCreateAfterAMovedJunctionRunsNoRollback(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	spelled := filepath.Join(m.P, "repo")
	if raw, _ := f.info(t, spelled, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	setStagedAddWindowsSteps(t, true)
	planted := filepath.Join(m.A, ".claude", "worktrees", "w2", ".git")
	stubStepsNow(t, "worktree,add", []string{"write", planted, "planted\n"})
	atStep(t, "answered", func() {
		if err := os.Rename(m.P, m.P+"old"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(filepath.Dir(m.B), m.P); err != nil {
			t.Error(err)
		}
	})
	leaf := filepath.Join(spelled, ".claude", "worktrees", "w2")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": spelled, "branchName": "w2", "worktreePath": leaf})
	if text, code := createErrorField(t, raw); text != plantedGitFileText(filepath.Join(m.A, ".claude", "worktrees", "w2")) || code != "unsafe_path" {
		t.Errorf("git.worktree_create = %s, want the refusal with no rollback part", raw)
	}
	if out := gitOut(t, m.B, "for-each-ref", "refs/heads/w2"); out == "" {
		t.Errorf("the branch of the second repository went: %s", raw)
	}
	if b, err := os.ReadFile(planted); err != nil || string(b) != "planted\n" {
		t.Errorf("the planted file changed: %q, %v", b, err)
	}
}

// moveJunctionAt moves the junction to the second repository when the remove reaches step.
func (f movedJunction) moveJunctionAt(t *testing.T, step string) {
	t.Helper()
	atStep(t, step, func() {
		if err := os.Rename(f.P, f.P+"old"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(filepath.Dir(f.B), f.P); err != nil {
			t.Error(err)
		}
	})
}

// wantSecondWhole fails when the live worktree w1 of the second repository lost its
// folder, its registration or its branch.
func (f movedJunction) wantSecondWhole(t *testing.T) {
	t.Helper()
	for _, p := range []string{filepath.Join(f.B, ".claude", "worktrees", "w1", ".git"), filepath.Join(f.B, ".git", "worktrees", "w1", "HEAD")} {
		if !exists(p) {
			t.Errorf("%s of the second repository went", p)
		}
	}
	if out := gitOut(t, f.B, "for-each-ref", "refs/heads/w1"); out == "" {
		t.Error("the branch of the second repository went")
	}
}

// removeW1 sends the remove of w1 through the junction.
func (f movedJunction) removeW1(t *testing.T) string {
	t.Helper()
	spelled := filepath.Join(f.P, "repo")
	return gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": spelled, "worktreePath": filepath.Join(spelled, ".claude", "worktrees", "w1"), "branchName": "w1"}))
}

// A gone leaf in the first repository, and the junction moves right after the locate
// to a second repository with a live w1. The reply is success. Only the ref of the same
// name goes in the second repository, and the entry of each repository stays
// (87375cc7, measured on Windows). Mutations: in removeGoneWorktree, refuse
// on the move. In removeGoneAfterMove, drop the branch step.
func TestRemoveOfAGoneLeafWithAMovedJunction(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A, f.B)
	if err := os.RemoveAll(filepath.Join(f.A, ".claude", "worktrees", "w1")); err != nil {
		t.Fatal(err)
	}
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.moveJunctionAt(t, "remove located")
	if got, want := f.removeW1(t), `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !exists(filepath.Join(f.B, ".claude", "worktrees", "w1", ".git")) || !exists(filepath.Join(f.B, ".git", "worktrees", "w1", "HEAD")) {
		t.Error("the leaf or the entry of the second repository went")
	}
	if !exists(filepath.Join(f.A, ".git", "worktrees", "w1", "HEAD")) {
		t.Error("the entry of the first repository went")
	}
	if out := gitOut(t, f.B, "for-each-ref", "refs/heads/w1"); out != "" {
		t.Errorf("the ref of the second repository stayed: %q", out)
	}
	if out := gitOut(t, f.A, "for-each-ref", "refs/heads/w1"); out == "" {
		t.Error("the branch of the first repository went")
	}
}

// A gone leaf whose entry is locked, in the first or in the second repository, and the
// junction moves after the locate. No row covers it, so nothing goes. Mutations: in
// removeGoneAfterMove, drop the lock test of that repository.
func TestRemoveOfAGoneLeafWithAMovedJunctionAndALock(t *testing.T) {
	for _, locked := range []string{"first", "second"} {
		t.Run(locked, func(t *testing.T) {
			f := newMovedJunction(t)
			addW1(t, f.A, f.B)
			repo := f.A
			if locked == "second" {
				repo = f.B
			}
			runGit(t, repo, "worktree", "lock", filepath.Join(repo, ".claude", "worktrees", "w1"))
			if err := os.RemoveAll(filepath.Join(f.A, ".claude", "worktrees", "w1")); err != nil {
				t.Fatal(err)
			}
			resetUserExcludesCache(t)
			clearDaemonConfigEnv(t)
			f.moveJunctionAt(t, "remove located")
			if got := f.removeW1(t); !strings.Contains(got, `"success":false`) {
				t.Errorf("got %s, want a refusal", got)
			}
			f.wantSecondWhole(t)
		})
	}
}

// swapDotGit puts a link to the git directory of the second repository at the `.git`
// of the first, when the remove reaches step.
func (f movedJunction) swapDotGit(t *testing.T, step string) {
	t.Helper()
	atStep(t, step, func() {
		g := filepath.Join(f.A, ".git")
		if err := os.Rename(g, g+".old"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(filepath.Join(f.B, ".git"), g); err != nil {
			t.Error(err)
		}
	})
}

// `.git` of baseRepo is replaced by a junction to the git directory of a second
// repository during the tree delete. The leaf goes, the registration stays, and no
// branch step runs (87375cc7, measured on Windows). Mutation: in
// gitWorktreeRemoveLocked, drop the holdLeftItsPath test.
func TestRemoveWithDotGitReplacedDuringTheDelete(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A, f.B)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.swapDotGit(t, "remove entry step")
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
		jsonString(t, "removed the worktree but could not drop its registration ("+realDirText(filepath.Join(f.A, ".git"))+")") + `,"branchKept":true}}`
	if got := f.removeW1(t); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if exists(filepath.Join(f.A, ".claude", "worktrees", "w1")) {
		t.Error("the leaf of the first repository stayed")
	}
	if !exists(filepath.Join(f.A, ".git.old", "worktrees", "w1", "HEAD")) {
		t.Error("the entry of the first repository went")
	}
	f.wantSecondWhole(t)
}

// `.git` is replaced after the test of the path and before the delete of the entry.
// The delete goes through the handle of the first git directory, so the entry of the
// second stays. Mutation: in gitWorktreeRemoveLocked, call dropWorktreeEntry in place
// of dropHeldEntry.
func TestRemoveDropsTheEntryThroughTheHeldGitDir(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A, f.B)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.swapDotGit(t, "remove entry drop")
	f.removeW1(t)
	if exists(filepath.Join(f.A, ".git.old", "worktrees", "w1")) {
		t.Error("the entry of the first repository stayed")
	}
	if !exists(filepath.Join(f.B, ".git", "worktrees", "w1", "HEAD")) {
		t.Error("the entry of the second repository went")
	}
}

// The same with a record that the verified-entry step does not take, so the delete is
// by the path of the record. Mutation: in gitWorktreeRemoveLocked, call
// dropWorktreeEntryByPath in place of dropHeldEntryByPath.
func TestRemoveDropsAnEntryByPathThroughTheHeldGitDir(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A, f.B)
	leaf := filepath.Join(f.A, ".claude", "worktrees", "w1")
	writeFile(t, filepath.Join(leaf, ".git"), "gitdir: "+filepath.Join(f.A, ".git", "worktrees", "other")+"\n", 0o644)
	writeFile(t, filepath.Join(f.B, ".git", "worktrees", "w1", "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.swapDotGit(t, "remove entry drop")
	f.removeW1(t)
	if exists(filepath.Join(f.A, ".git.old", "worktrees", "w1")) {
		t.Error("the entry of the first repository stayed")
	}
	if !exists(filepath.Join(f.B, ".git", "worktrees", "w1", "HEAD")) {
		t.Error("the entry of the second repository went")
	}
}

// A gone leaf with a `.git` replaced before its registration step keeps every entry.
// Mutation: in removeGoneWorktree, drop the holdLeftItsPath test.
func TestRemoveOfAGoneLeafWithDotGitReplaced(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A, f.B)
	if err := os.RemoveAll(filepath.Join(f.A, ".claude", "worktrees", "w1")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.B, ".git", "worktrees", "w1", "gitdir"), filepath.Join(f.A, ".claude", "worktrees", "w1", ".git")+"\n", 0o644)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.swapDotGit(t, "remove entry step")
	if got := f.removeW1(t); !strings.Contains(got, `"success":false`) {
		t.Errorf("got %s, want a refusal", got)
	}
	if !exists(filepath.Join(f.A, ".git.old", "worktrees", "w1", "HEAD")) || !exists(filepath.Join(f.B, ".git", "worktrees", "w1", "HEAD")) {
		t.Error("an entry went")
	}
}

// A folder on the real path of baseRepo denies List, so filepath.EvalSymlinks fails
// there. git.status and git.list_branches answer no repository with no git call, and a
// create and a remove refuse as no trust root (87375cc7, measured on Windows).
// A denied folder past a junction is off the real path.
// Mutation: in linkWalkFails and statusBaseWalkFails, drop realPathUnlistable.
func TestUnlistableFolderOnTheRealPath(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	oldEval := evalSymlinks
	t.Cleanup(func() { evalSymlinks = oldEval })
	evalSymlinks = func(p string) (string, error) {
		if strings.HasPrefix(p, f.A) {
			return "", &os.PathError{Op: "FindFirstFile", Path: p, Err: os.ErrPermission}
		}
		return oldEval(p)
	}
	spelled := filepath.Join(f.P, "repo")
	leaf := filepath.Join(spelled, ".claude", "worktrees", "w1")
	d := newTestGitDeps()
	for _, c := range []struct {
		method string
		params map[string]any
		want   string
	}{
		{"git.status", map[string]any{"path": spelled, "baseRepo": spelled}, `"isRepo":false`},
		{"git.list_branches", map[string]any{"path": spelled}, `"branches":[]`},
		{"git.worktree_create", map[string]any{"baseRepo": spelled, "branchName": "w2", "worktreePath": filepath.Join(spelled, ".claude", "worktrees", "w2")}, `"errorCode":"nested_base_repo"`},
		{"git.worktree_remove", map[string]any{"baseRepo": spelled, "worktreePath": leaf, "branchName": "w1"}, jsonString(t, managedWorktreesRefusal)},
	} {
		if got := gitDispatch(t, d, rpcLine(t, c.method, c.params)); !strings.Contains(got, c.want) {
			t.Errorf("%s = %s, want %s", c.method, got, c.want)
		}
	}
	if !exists(filepath.Join(f.A, ".claude", "worktrees", "w1", ".git")) || !exists(filepath.Join(f.A, ".git", "worktrees", "w1", "HEAD")) {
		t.Error("the worktree went")
	}
	if exists(filepath.Join(f.A, ".claude", "worktrees", "w2")) {
		t.Error("the create made a leaf")
	}
	evalSymlinks = oldEval
	if realPathUnlistable(spelled) {
		t.Error("a real path that resolves counts as denied")
	}
}

// git.list_branches refuses a path through a link to a device path with no git call
// (87375cc7, measured on Windows). Mutation: in
// gitListBranches, drop the linkHopRefusal test.
func TestListBranchesThroughADeviceLink(t *testing.T) {
	f := newMovedJunction(t)
	fakeLinkTargets(t, map[string]string{f.P: `\??\Volume{00000000-0000-0000-0000-000000000001}\work`})
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	path := filepath.Join(f.P, "repo")
	got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.list_branches", map[string]any{"path": path}))
	if want := errFrame(t, networkLinkText(path)); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A locked worktree in the first repository, and the junction moves right after the
// locate. The lock of the first repository is read and refuses, and nothing goes in
// either repository. Mutation: in gitWorktreeRemoveLocked, take the pin after the
// "remove located" step (the locked tree and the entry of the second go).
func TestRemoveOfALockedLeafWithAMovedJunction(t *testing.T) {
	f := newMovedJunction(t)
	for _, repo := range []string{f.A, f.B} {
		runGit(t, repo, "worktree", "add", "-q", "-b", "w1", filepath.Join(repo, ".claude", "worktrees", "w1"))
	}
	runGit(t, f.A, "worktree", "lock", filepath.Join(f.A, ".claude", "worktrees", "w1"))
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.moveJunctionAt(t, "remove located")
	if got := f.removeW1(t); !strings.Contains(got, `"success":false`) || !strings.Contains(got, "locked") {
		t.Errorf("got %s, want the locked refusal", got)
	}
	if !exists(filepath.Join(f.A, ".claude", "worktrees", "w1", ".git")) || !exists(filepath.Join(f.A, ".git", "worktrees", "w1", "locked")) {
		t.Error("the locked worktree of the first repository went")
	}
	f.wantSecondWhole(t)
}

// The junction moves between the pin and the locate, so the open folder is in the
// second repository. The remove refuses with no delete. Mutation: in
// gitWorktreeRemoveLocked, drop the movedSincePin test after the locate.
func TestRemoveWithAJunctionMovedBeforeTheLocate(t *testing.T) {
	f := newMovedJunction(t)
	for _, repo := range []string{f.A, f.B} {
		runGit(t, repo, "worktree", "add", "-q", "-b", "w1", filepath.Join(repo, ".claude", "worktrees", "w1"))
	}
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.moveJunctionAt(t, "remove pinned")
	spelled := filepath.Join(f.P, "repo", ".claude", "worktrees", "w1")
	if got, want := f.removeW1(t), spellRemoveFrame(t, lockCheckRefusal(spelled)); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !exists(filepath.Join(f.A, ".claude", "worktrees", "w1", ".git")) || !exists(filepath.Join(f.A, ".git", "worktrees", "w1", "HEAD")) {
		t.Error("the worktree of the first repository went")
	}
	f.wantSecondWhole(t)
}

// A gone leaf, and the junction moves right before the branch step. The branch of the
// second repository stays. Mutation: in removeGoneWorktree, drop the movedSincePin
// test before the branch step.
func TestRemoveOfAGoneLeafMovedBeforeTheBranchStep(t *testing.T) {
	f := newMovedJunction(t)
	runGit(t, f.B, "worktree", "add", "-q", "-b", "w1", filepath.Join(f.B, ".claude", "worktrees", "w1"))
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.moveJunctionAt(t, "remove branch step")
	if got, want := f.removeW1(t), `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	f.wantSecondWhole(t)
}

// replaceLink points the stand-in junction link at to.
func replaceLink(t *testing.T, link, to string) {
	t.Helper()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(to, link); err != nil {
		t.Fatal(err)
	}
}

// addW1 adds the live worktree w1 to each repository.
func addW1(t *testing.T, repos ...string) {
	t.Helper()
	for _, repo := range repos {
		runGit(t, repo, "worktree", "add", "-q", "-b", "w1", filepath.Join(repo, ".claude", "worktrees", "w1"))
	}
}

// The junction moves to the second repository after the pin, the locate opens the
// folder there, and the junction moves back. The path names the pinned folder again,
// and the open folder does not. Nothing is deleted. Mutation: in
// gitWorktreeRemoveLocked, drop the openedOtherBase test.
func TestRemoveWithAMoveAndBackAroundTheLocate(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A, f.B)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	atStep(t, "remove pinned", func() { replaceLink(t, f.P, filepath.Dir(f.B)) })
	old := removeParentOpener
	t.Cleanup(func() { removeParentOpener = old })
	removeParentOpener = func(base, dirRel, leaf string) (removeTarget, error) {
		target, err := old(base, dirRel, leaf)
		replaceLink(t, f.P, filepath.Dir(f.A))
		return target, err
	}
	spelled := filepath.Join(f.P, "repo", ".claude", "worktrees", "w1")
	if got, want := f.removeW1(t), spellRemoveFrame(t, lockCheckRefusal(spelled)); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !exists(filepath.Join(f.A, ".claude", "worktrees", "w1", ".git")) || !exists(filepath.Join(f.A, ".git", "worktrees", "w1", "HEAD")) {
		t.Error("the worktree of the first repository went")
	}
	f.wantSecondWhole(t)
}

// A junction to a folder with no repository at the pin, which moves to a second
// repository after the locate. Mutation: in pinBeforeLocate, set removePinned only for
// a trusted git directory.
func TestRemoveWithNoRepositoryAtThePin(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.B)
	if err := os.RemoveAll(filepath.Join(f.A, ".git")); err != nil {
		t.Fatal(err)
	}
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.moveJunctionAt(t, "remove located")
	if got := f.removeW1(t); !strings.Contains(got, `"success":false`) {
		t.Errorf("got %s, want a refusal", got)
	}
	f.wantSecondWhole(t)
}

// plainBase is a repository at <holder>/repo whose holder folder is plain at the pin.
// The move renames the holder to kept and puts a link to the second repository there.
func plainBase(t *testing.T, f movedJunction) (base, kept string) {
	t.Helper()
	holder := filepath.Join(filepath.Dir(f.P), "holder")
	base, kept = filepath.Join(holder, "repo"), filepath.Join(filepath.Dir(f.P), "kept")
	initTrustMain(t, base)
	atStep(t, "remove located", func() {
		if err := os.Rename(holder, kept); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(filepath.Dir(f.B), holder); err != nil {
			t.Error(err)
		}
	})
	return base, kept
}

// A plain baseRepo with a gone leaf that becomes a link to a second repository after
// the locate. Mutation: in pinBeforeLocate, return when the path holds no junction.
func TestRemoveOfAPlainBaseThatMovesWithAGoneLeaf(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.B)
	base, _ := plainBase(t, f)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": base, "worktreePath": filepath.Join(base, ".claude", "worktrees", "w1"), "branchName": "w1"}))
	if !strings.Contains(got, `"success":false`) {
		t.Errorf("got %s, want a refusal", got)
	}
	f.wantSecondWhole(t)
}

// A plain baseRepo with a live locked worktree that becomes a link to a second
// repository after the locate. The path of the pin then names the second repository,
// so no lock can be read. The locked tree and the second repository stay. Mutation: in
// gitWorktreeRemoveLocked, drop the pinStands test.
func TestRemoveOfAPlainBaseThatMovesWithALockedLeaf(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.B)
	base, kept := plainBase(t, f)
	leaf := filepath.Join(base, ".claude", "worktrees", "w1")
	runGit(t, base, "worktree", "add", "-q", "-b", "w1", leaf)
	runGit(t, base, "worktree", "lock", leaf)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": base, "worktreePath": leaf, "branchName": "w1"}))
	if !strings.Contains(got, `"success":false`) {
		t.Errorf("got %s, want a refusal", got)
	}
	if !exists(filepath.Join(kept, "repo", ".claude", "worktrees", "w1", ".git")) || !exists(filepath.Join(kept, "repo", ".git", "worktrees", "w1", "locked")) {
		t.Error("the locked worktree of the first repository went")
	}
	f.wantSecondWhole(t)
}

// A live leaf, and the junction moves right before the branch step to a second
// repository whose gate refuses. The gate runs and its ref stays. Mutation: in
// gitWorktreeRemoveLocked, test junctionMoved alone before the branch step.
func TestRemoveWithAJunctionMovedBeforeTheBranchStep(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A, f.B)
	exclude := filepath.Join(f.B, ".git", "info", "exclude")
	if err := os.RemoveAll(exclude); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(exclude, 0o755); err != nil {
		t.Fatal(err)
	}
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.moveJunctionAt(t, "remove branch step")
	if got, want := f.removeW1(t), `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	f.wantSecondWhole(t)
}

// A baseRepo that is missing at the pin, and a move right before the branch step of
// that path puts a second repository there. Its branch stays. Mutation: in
// missingDone, drop the movedSincePin test.
func TestRemoveOfAMissingBaseMovedBeforeTheBranchStep(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.B)
	if err := os.RemoveAll(f.A); err != nil {
		t.Fatal(err)
	}
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.moveJunctionAt(t, "remove branch step")
	if got, want := f.removeW1(t), `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	f.wantSecondWhole(t)
}

// `.git` is replaced right before the branch step, after the entry went. The folder of
// baseRepo did not move, so only the test of the hold sees it. The ref of the second
// repository stays. Mutation: in gitWorktreeRemoveLocked, drop the holdLeftItsPath test
// before the branch step.
func TestRemoveWithDotGitReplacedBeforeTheBranchStep(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A, f.B)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.swapDotGit(t, "remove branch step")
	if got, want := f.removeW1(t), `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if exists(filepath.Join(f.A, ".git.old", "worktrees", "w1")) {
		t.Error("the entry of the first repository stayed")
	}
	f.wantSecondWhole(t)
}

// The same for a gone leaf. Mutation: in removeGoneWorktree, test movedSincePin alone
// before the branch step.
func TestRemoveOfAGoneLeafWithDotGitReplacedBeforeTheBranchStep(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A, f.B)
	if err := os.RemoveAll(filepath.Join(f.A, ".claude", "worktrees", "w1")); err != nil {
		t.Fatal(err)
	}
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.swapDotGit(t, "remove branch step")
	if got, want := f.removeW1(t), `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	f.wantSecondWhole(t)
}

// separateGitDir moves the git directory of the first repository to a folder Z, leaves
// a `.git` file that names it, sets the daemon GIT_COMMON_DIR to Z and adds w1.
func (f movedJunction) separateGitDir(t *testing.T) string {
	t.Helper()
	z := filepath.Join(filepath.Dir(f.A), "Z")
	if err := os.Rename(filepath.Join(f.A, ".git"), z); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.A, ".git"), "gitdir: "+z+"\n", 0o644)
	addW1(t, f.A)
	t.Setenv("GIT_COMMON_DIR", z)
	return z
}

// With a daemon GIT_COMMON_DIR and a `.git` file, a link to a second repository is put
// at `.git` during the tree delete. The leaf and the entry in the separate git directory
// go, the branch step is refused, and the second repository stays whole (87375cc7,
// measured on Windows). Mutation: in gitWorktreeRemoveLocked, drop the dotGitBecameLink
// test.
func TestRemoveWithADaemonCommonDirAndDotGitReplaced(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.B)
	z := f.separateGitDir(t)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.swapDotGit(t, "remove entry step")
	dotGit := filepath.Join(f.P, "repo", ".git")
	text := `removed the worktree, but left its branch "w1" in place: ` + repoConfigPrefix + gitDirLinkText(dotGit)
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, text) + `,"branchKept":true}}`
	if got := f.removeW1(t); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if exists(filepath.Join(f.A, ".claude", "worktrees", "w1")) || exists(filepath.Join(z, "worktrees", "w1")) {
		t.Error("the leaf or the entry of the first repository stayed")
	}
	f.wantSecondWhole(t)
}

// The same swap with a gone leaf, which no row covers. The second repository stays
// whole. Mutation: in removeGoneWorktree, drop the dotGitBecameLink test.
func TestRemoveOfAGoneLeafWithADaemonCommonDirAndDotGitReplaced(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.B)
	f.separateGitDir(t)
	if err := os.RemoveAll(filepath.Join(f.A, ".claude", "worktrees", "w1")); err != nil {
		t.Fatal(err)
	}
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	f.swapDotGit(t, "remove branch step")
	if got, want := f.removeW1(t), `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	f.wantSecondWhole(t)
}

// With "shadowDirOff":true and a link put at `.git` during the tree delete, the reply
// has the registration text and no branchKept. The ref of the same name goes in the
// second repository, also for a locked worktree there. Its entry, leaf and lock stay,
// and so do the entry and the branch of the first (87375cc7, measured on Windows).
// Mutations: in gitWorktreeRemoveLocked, return on the swap also with the off switch.
// Drop the offSwapped test of the entry delete, or of the branch step.
func TestRemoveWithTheOffSwitchAndDotGitReplaced(t *testing.T) {
	for _, locked := range []bool{false, true} {
		name := "plain"
		if locked {
			name = "locked"
		}
		t.Run(name, func(t *testing.T) {
			f := newMovedJunction(t)
			addW1(t, f.A, f.B)
			if locked {
				runGit(t, f.B, "worktree", "lock", filepath.Join(f.B, ".claude", "worktrees", "w1"))
			}
			resetUserExcludesCache(t)
			clearDaemonConfigEnv(t)
			f.swapDotGit(t, "remove entry step")
			spelled := filepath.Join(f.P, "repo")
			got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{"baseRepo": spelled,
				"worktreePath": filepath.Join(spelled, ".claude", "worktrees", "w1"), "branchName": "w1", "shadowDirOff": true}))
			text := "removed the worktree but could not drop its registration (" + realDirText(filepath.Join(f.A, ".git")) + ")"
			if want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, text) + `}}`; got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			if out := gitOut(t, f.B, "for-each-ref", "refs/heads/w1"); out != "" {
				t.Errorf("the ref of the second repository stayed: %q", out)
			}
			for _, p := range []string{filepath.Join(f.B, ".claude", "worktrees", "w1", ".git"), filepath.Join(f.B, ".git", "worktrees", "w1", "HEAD"),
				filepath.Join(f.A, ".git.old", "worktrees", "w1", "HEAD"), filepath.Join(f.A, ".git.old", "refs", "heads", "w1")} {
				if !exists(p) {
					t.Errorf("%s went", p)
				}
			}
			if locked && !exists(filepath.Join(f.B, ".git", "worktrees", "w1", "locked")) {
				t.Error("the lock of the second repository went")
			}
		})
	}
}

// createFailsAfterAMove runs a create whose read-tree fails while the junction moves to
// the second repository at the start of the rollback. It returns the reply.
func createFailsAfterAMove(t *testing.T, f shadowFixture, m movedJunction) string {
	t.Helper()
	spelled := filepath.Join(m.P, "repo")
	if raw, _ := f.info(t, spelled, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	setStagedAddWindowsSteps(t, true)
	slowGit(t, "read-tree", "fail", 0, `fatal: injected failure\n`, "")
	m.moveJunctionAt(t, "undo")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": spelled, "branchName": "w1",
		"worktreePath": filepath.Join(spelled, ".claude", "worktrees", "w1")})
	if text, code := createErrorField(t, raw); text != "git worktree add failed (checkout): fatal: injected failure" || code != "worktree_add_failed" {
		t.Errorf("git.worktree_create = %s", raw)
	}
	if strings.Contains(raw, "branchKept") {
		t.Errorf("the reply has branchKept: %s", raw)
	}
	if exists(filepath.Join(m.A, ".claude", "worktrees", "w1")) || exists(filepath.Join(m.A, ".git", "worktrees", "w1")) {
		t.Error("the leaf or the entry of the first repository stayed")
	}
	if out := gitOut(t, m.A, "for-each-ref", "refs/heads/w1"); out == "" {
		t.Error("the branch of the first repository went")
	}
	return raw
}

// A failed read-tree with a junction moved to a second repository that has a locked
// worktree of the same name with an empty leaf. Only its ref goes (87375cc7, measured
// on Windows). Mutations: in undoFailedCheckout, drop the acrossMove step (the undo text
// comes back). Set leaf to worktreePath there (the first leaf stays). Drop the runBranchStep call of acrossMove (the ref stays).
func TestFailedCreateAfterAMovedJunctionDropsTheSecondRef(t *testing.T) {
	for name, locked := range map[string]bool{"live": false, "locked": true} {
		t.Run(name, func(t *testing.T) {
			f, m := newShadowMovedJunction(t)
			leafB := emptiedSecondLeaf(t, m, locked)
			createFailsAfterAMove(t, f, m)
			if out := gitOut(t, m.B, "for-each-ref", "refs/heads/w1"); out != "" {
				t.Errorf("the ref of the second repository stayed: %q", out)
			}
			if !exists(leafB) || !exists(filepath.Join(m.B, ".git", "worktrees", "w1", "HEAD")) {
				t.Error("the leaf or the entry of the second repository went")
			}
			if locked && !exists(filepath.Join(m.B, ".git", "worktrees", "w1", "locked")) {
				t.Error("the lock of the second repository went")
			}
		})
	}
}

// emptiedSecondLeaf adds the worktree w1 to the second repository and empties its leaf.
func emptiedSecondLeaf(t *testing.T, m movedJunction, locked bool) string {
	t.Helper()
	leafB := filepath.Join(m.B, ".claude", "worktrees", "w1")
	// A tip of its own, as in the measured second repository.
	runGit(t, m.B, "commit", "-q", "--allow-empty", "-m", "second")
	runGit(t, m.B, "worktree", "add", "-q", "-b", "w1", leafB)
	if locked {
		runGit(t, m.B, "worktree", "lock", leafB)
	}
	names, err := os.ReadDir(leafB)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range names {
		if err := os.RemoveAll(filepath.Join(leafB, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	return leafB
}

// A second repository whose gate refuses keeps its ref (claustrum's own, not measured).
// Its `info/exclude` is a folder. Mutation: in undoFailedCheckout, run the branch step of
// acrossMove with a gate that refused.
func TestFailedCreateAfterAMovedJunctionKeepsARefusedRef(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	emptiedSecondLeaf(t, m, false)
	exclude := filepath.Join(m.B, ".git", "info", "exclude")
	if err := os.RemoveAll(exclude); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(exclude, 0o755); err != nil {
		t.Fatal(err)
	}
	createFailsAfterAMove(t, f, m)
	if out := gitOut(t, m.B, "for-each-ref", "refs/heads/w1"); out == "" {
		t.Error("the ref of the refused second repository went")
	}
}

// The same failure with only an empty folder at the leaf of the second repository, which
// has no ref of that name. Nothing goes there (87375cc7, measured on Windows). Mutation:
// in undoFailedCheckout, give removeEmptyLeaf worktreePath (the empty folder goes).
func TestFailedCreateAfterAMovedJunctionKeepsAnEmptyFolder(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	leafB := filepath.Join(m.B, ".claude", "worktrees", "w1")
	if err := os.MkdirAll(leafB, 0o755); err != nil {
		t.Fatal(err)
	}
	createFailsAfterAMove(t, f, m)
	if !exists(leafB) {
		t.Error("the empty folder of the second repository went")
	}
	if out := gitOut(t, m.B, "for-each-ref", "refs/heads/"); strings.Contains(out, "w1") {
		t.Errorf("the second repository has a ref w1: %q", out)
	}
}

// createWithMoveBeforeReadTree moves the junction right before a read-tree call that
// runs and succeeds, and checks the state of the first repository.
func createWithMoveBeforeReadTree(t *testing.T, f shadowFixture, m movedJunction) {
	t.Helper()
	commitOwnFile(t, m.A, "from the first repository\n")
	spelled := filepath.Join(m.P, "repo")
	if raw, _ := f.info(t, spelled, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	setStagedAddWindowsSteps(t, true)
	// The call right before the read-tree. A move there reaches the start of a read-tree
	// that names the spelled folder.
	stubStepsNow(t, "--get-regexp", m.moveSteps()...)
	log := filepath.Join(t.TempDir(), "envlog")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVLOG", log)
	leaf := filepath.Join(spelled, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": spelled, "branchName": "w1", "worktreePath": leaf})
	if text, code := createErrorField(t, raw); text != leafMovedText(leaf) || code != "unsafe_path" {
		t.Errorf("git.worktree_create = %s", raw)
	}
	leafA := filepath.Join(m.A, ".claude", "worktrees", "w1")
	wantReadTreeIn(t, log, leafA)
	wantPairWorkTree(t, log, m.A)
	tracked := strings.Fields(gitOut(t, m.A, "ls-files"))
	if len(tracked) == 0 {
		t.Fatal("the first repository tracks no file")
	}
	for _, name := range tracked {
		if !exists(filepath.Join(leafA, filepath.FromSlash(name))) {
			t.Errorf("the leaf of the first repository has no %s", name)
		}
	}
	if !exists(filepath.Join(m.A, ".git", "worktrees", "w1", "index")) {
		t.Error("the entry of the first repository has no index")
	}
	if out := gitOut(t, m.A, "for-each-ref", "refs/heads/w1"); out == "" {
		t.Error("the branch of the first repository went")
	}
}

// wantReadTreeIn fails unless the one read-tree call of the stub log ran in leaf, with
// leaf as its --work-tree.
func wantReadTreeIn(t *testing.T, log, leaf string) {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, rec := range strings.Split(string(b), "\x1d\n") {
		// The environment holds the separator too, in the steps of the stub.
		parts := strings.SplitN(rec, "\x1e", 3)
		if len(parts) != 3 || !slices.Contains(strings.Split(parts[1], "\x1f"), "read-tree") {
			continue
		}
		found++
		if parts[0] != leaf || !slices.Contains(strings.Split(parts[1], "\x1f"), "--work-tree="+leaf) {
			t.Errorf("read-tree ran in %s with %q, want %s", parts[0], parts[1], leaf)
		}
	}
	if found != 1 {
		t.Errorf("%d read-tree calls, want 1", found)
	}
}

// wantPairWorkTree fails unless the show-toplevel pair of the stub log names want.
func wantPairWorkTree(t *testing.T, log, want string) {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, rec := range strings.Split(string(b), "\x1d\n") {
		parts := strings.SplitN(rec, "\x1e", 3)
		if len(parts) != 3 || !strings.Contains(parts[1], "--show-toplevel") || !strings.Contains(parts[1], "--work-tree=") {
			continue
		}
		found++
		if !slices.Contains(strings.Split(parts[1], "\x1f"), "--work-tree="+want) {
			t.Errorf("the pair has %q, want --work-tree=%s", parts[1], want)
		}
	}
	if found == 0 {
		t.Error("no show-toplevel pair ran")
	}
}

// commitOwnFile commits the tracked file own.txt in repo.
func commitOwnFile(t *testing.T, repo, content string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, "own.txt"), content, 0o644)
	runGit(t, repo, "add", "own.txt")
	runGit(t, repo, "commit", "-q", "-m", "own")
}

// A junction moved to a second repository with a live worktree of the same name, right
// before a read-tree that succeeds. The tree of the first repository lands in its own
// leaf, the modified file of the second stays, and nothing is rolled back (87375cc7,
// measured on Windows). Mutations: make checkoutFolder return leaf, or make
// indexStepRefused return u.swapped(step) alone.
func TestReadTreeAfterAMovedJunctionStaysInTheFirstLeaf(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	leafB := filepath.Join(m.B, ".claude", "worktrees", "w1")
	commitOwnFile(t, m.B, "from the second repository\n")
	runGit(t, m.B, "worktree", "add", "-q", "-b", "w1", leafB)
	changed := filepath.Join(leafB, "own.txt")
	writeFile(t, changed, "modified in the second repository\n", 0o644)
	before, err := os.ReadDir(leafB)
	if err != nil {
		t.Fatal(err)
	}
	createWithMoveBeforeReadTree(t, f, m)
	after, err := os.ReadDir(leafB)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the leaf of the second repository went from %d to %d entries", len(before), len(after))
	}
	if b, err := os.ReadFile(changed); err != nil || string(b) != "modified in the second repository\n" {
		t.Errorf("the file of the second repository changed: %q, %v", b, err)
	}
	if out := gitOut(t, m.B, "for-each-ref", "refs/heads/w1"); out == "" {
		t.Error("the ref of the second repository went")
	}
}

// The same move with no worktree of that name in the second repository. Nothing lands
// there (87375cc7, measured on Windows). Mutation: make checkoutFolder return leaf.
func TestReadTreeAfterAMovedJunctionMakesNoSecondLeaf(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	createWithMoveBeforeReadTree(t, f, m)
	if exists(filepath.Join(m.B, ".claude", "worktrees", "w1")) || exists(filepath.Join(m.B, ".git", "worktrees", "w1")) {
		t.Error("the second repository has a leaf or an entry")
	}
}

// The same move with a plain folder at the leaf of a second repository, which holds no
// `.git` entry. Mutation: drop the behindMovedJunction test before leafGitFileGone (the
// reply is the text of a leaf with no `.git` file).
func TestReadTreeAfterAMovedJunctionWithAPlainSecondLeaf(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	marker := filepath.Join(m.B, ".claude", "worktrees", "w1", "marker.txt")
	writeFile(t, marker, "marker\n", 0o644)
	createWithMoveBeforeReadTree(t, f, m)
	if b, err := os.ReadFile(marker); err != nil || string(b) != "marker\n" {
		t.Errorf("the marker changed: %q, %v", b, err)
	}
	if exists(filepath.Join(filepath.Dir(marker), ".git")) {
		t.Error("the plain folder got a .git entry")
	}
}

// The same move to a folder with no git directory at all, with and without a leaf.
// No rollback runs (87375cc7, measured on Windows). Mutation: make behindMovedJunction
// call junctionLeadsTo (the index step refuses, and the undo text comes back).
func TestReadTreeAfterAJunctionMovedToAPlainFolder(t *testing.T) {
	for name, withLeaf := range map[string]bool{"leaf": true, "no leaf": false} {
		t.Run(name, func(t *testing.T) {
			f, m := newShadowMovedJunction(t)
			if err := os.RemoveAll(filepath.Join(m.B, ".git")); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(m.B, "top-marker.txt")
			writeFile(t, marker, "marker\n", 0o644)
			if withLeaf {
				writeFile(t, filepath.Join(m.B, ".claude", "worktrees", "w1", "marker.txt"), "marker\n", 0o644)
			}
			createWithMoveBeforeReadTree(t, f, m)
			if !exists(filepath.Join(m.A, ".git", "worktrees", "w1", "gitdir")) {
				t.Error("the entry of the first repository went")
			}
			if exists(filepath.Join(m.B, ".git")) || !exists(marker) {
				t.Error("the second folder changed")
			}
			if got := exists(filepath.Join(m.B, ".claude", "worktrees", "w1")); got != withLeaf {
				t.Errorf("the second leaf exists = %v", got)
			}
		})
	}
}

// After the move at `rev-parse --absolute-git-dir` the pair names the leaf below the top
// of the held git directory (87375cc7, measured on Windows, one row). Mutation: in
// resolvedPairTree, drop the junctionMoved arm.
func TestPairAfterAJunctionMoveNamesTheLeaf(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	spelled := filepath.Join(m.P, "repo")
	if raw, _ := f.info(t, spelled, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	setStagedAddWindowsSteps(t, true)
	m.moveJunctionAt(t, "answered")
	log := filepath.Join(t.TempDir(), "envlog")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVLOG", log)
	leaf := filepath.Join(spelled, ".claude", "worktrees", "w2")
	f.call(t, "git.worktree_create", map[string]any{"baseRepo": spelled, "branchName": "w2", "worktreePath": leaf})
	wantPairWorkTree(t, log, filepath.Join(m.A, ".claude", "worktrees", "w2"))
}

// A `.git` replaced during the tree delete makes one more `rev-parse --git-dir` of the
// gate (87375cc7, measured on Windows, 10 of 10). Mutation: in gitWorktreeRemoveLocked,
// drop the askGit call of the held-left refusal.
func TestRemoveAsksGitAgainWhenDotGitLeft(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	for _, repo := range []string{m.A, m.B} {
		runGit(t, repo, "worktree", "add", "-q", "-b", "w1", filepath.Join(repo, ".claude", "worktrees", "w1"))
	}
	log := filepath.Join(t.TempDir(), "envlog")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVLOG", log)
	m.swapDotGit(t, "remove entry step")
	spelled := filepath.Join(m.P, "repo")
	raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": spelled,
		"worktreePath": filepath.Join(spelled, ".claude", "worktrees", "w1"), "branchName": "w1"})
	if !strings.Contains(raw, "could not drop its registration") {
		t.Fatalf("git.worktree_remove = %s", raw)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	asks := 0
	for _, rec := range strings.Split(string(b), "\x1d\n") {
		if parts := strings.SplitN(rec, "\x1e", 3); len(parts) == 3 && strings.HasSuffix(parts[1], "rev-parse\x1f--git-dir") {
			asks++
		}
	}
	if asks != 2 {
		t.Errorf("%d calls of rev-parse --git-dir, want 2", asks)
	}
}

// A config listing stopped by its limit, with the junction moved at the rollback. No
// read-tree call ran, so the ref of the second repository stays. Mutation: in the failed
// read-tree arm, set afterJunctionMove with no test of the exit error.
func TestStoppedListingKeepsTheSecondRef(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	emptiedSecondLeaf(t, m, false)
	spelled := filepath.Join(m.P, "repo")
	if raw, _ := f.info(t, spelled, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	setStagedAddWindowsSteps(t, true)
	endHeldDescendant(t)
	log := stubLog(t)
	t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "")
	setDuration(t, &configListingLimit, testCallLimit)
	old := createStepHook
	t.Cleanup(func() { createStepHook = old })
	armed, movedNow := false, false
	createStepHook = func(at string) {
		if at == "staged" && !armed {
			armed = true
			t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "hold-full")
		}
		if at == "undo" && !movedNow {
			movedNow = true
			t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "")
			if err := os.Rename(m.P, m.P+"old"); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(filepath.Dir(m.B), m.P); err != nil {
				t.Error(err)
			}
		}
	}
	f.call(t, "git.worktree_create", map[string]any{"baseRepo": spelled, "branchName": "w1",
		"worktreePath": filepath.Join(spelled, ".claude", "worktrees", "w1")})
	if !armed || !movedNow {
		t.Fatalf("the steps did not run: armed %v, moved %v", armed, movedNow)
	}
	if n := len(gitCalls(t, log, "read-tree")); n != 0 {
		t.Fatalf("%d read-tree calls ran, want 0", n)
	}
	if out := gitOut(t, m.B, "for-each-ref", "refs/heads/w1"); out == "" {
		t.Error("the ref of the second repository went, and no read-tree ran")
	}
}

// The junction moves to the second repository during the pin of a remove and comes back
// before the locate. The second repository keeps its entry. Mutation: in pinBeforeLocate,
// give requestGitDirTrust the spelled repo.
func TestRemoveWithAMoveAndBackDuringThePin(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A, f.B)
	spelled := filepath.Join(f.P, "repo")
	leaf := filepath.Join(spelled, ".claude", "worktrees", "w1")
	entryB := filepath.Join(f.B, ".git", "worktrees", "w1")
	writeFile(t, filepath.Join(entryB, "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	oldHolds := pathHoldsLink
	t.Cleanup(func() { pathHoldsLink = oldHolds })
	moved := false
	pathHoldsLink = func(p string) bool {
		if !moved {
			if s := string(debug.Stack()); strings.Contains(s, "pinBeforeLocate") && strings.Contains(s, "requestGitDirTrust") {
				moved = true
				if err := os.Rename(f.P, f.P+"old"); err != nil {
					t.Error(err)
				}
				if err := os.Symlink(filepath.Dir(f.B), f.P); err != nil {
					t.Error(err)
				}
			}
		}
		return oldHolds(p)
	}
	atStep(t, "remove pinned", func() {
		if !moved {
			t.Error("the move did not run")
			return
		}
		if err := os.Remove(f.P); err != nil {
			t.Error(err)
		}
		if err := os.Rename(f.P+"old", f.P); err != nil {
			t.Error(err)
		}
	})
	got := f.removeW1(t)
	if !exists(filepath.Join(entryB, "HEAD")) {
		t.Errorf("the registration of the second repository went: %s", got)
	}
}

// A gone leaf with a second repository whose gate refuses. Its `info/exclude` is a
// folder. The ref stays, and the reply has the member of a kept branch. Mutation: in
// removeGoneAfterMove, go on after a movedBranchGate that refused.
func TestRemoveOfAGoneLeafKeepsARefusedSecondRef(t *testing.T) {
	f, m := newShadowMovedJunction(t)
	addW1(t, m.A, m.B)
	if err := os.RemoveAll(filepath.Join(m.A, ".claude", "worktrees", "w1")); err != nil {
		t.Fatal(err)
	}
	// The folder takes the place of the file after the first gate of the gone leaf, right
	// after its `rev-parse --absolute-git-dir` in the second repository.
	exclude := filepath.Join(m.B, ".git", "info", "exclude")
	folder := exclude + ".folder"
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	stubStepsNow(t, "rev-parse,--absolute-git-dir", []string{"rename", exclude, exclude + ".old"}, []string{"rename", folder, exclude})
	m.moveJunctionAt(t, "remove located")
	spelled := filepath.Join(m.P, "repo")
	raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": spelled,
		"worktreePath": filepath.Join(spelled, ".claude", "worktrees", "w1"), "branchName": "w1"})
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`; raw != want {
		t.Errorf("git.worktree_remove = %s\nwant %s", raw, want)
	}
	if out := gitOut(t, m.B, "for-each-ref", "refs/heads/w1"); out == "" {
		t.Error("the ref of the refused second repository went")
	}
	if fi, err := os.Stat(exclude); err != nil || !fi.IsDir() {
		t.Errorf("the folder did not take the place of the file: %v", err)
	}
}

// Mutations: drop the test of stagedAddWindowsSteps, or of an empty resolved path.
func TestCheckoutFolderFollowsTheWindowsSteps(t *testing.T) {
	if got := checkoutFolder("/spelled", "/real"); got != "/spelled" {
		t.Errorf("off: %q", got)
	}
	setStagedAddWindowsSteps(t, true)
	if got := checkoutFolder("/spelled", "/real"); got != "/real" {
		t.Errorf("on: %q", got)
	}
	if got := checkoutFolder("/spelled", ""); got != "/spelled" {
		t.Errorf("on with no resolved path: %q", got)
	}
}

// On Linux and macOS the pin of a remove stays off. Mutation: drop the test of
// walkStartsAsSpelled in pinBeforeLocate.
func TestPinStaysOffWithNoWindowsWalk(t *testing.T) {
	repo := resolveTestRoot(t, t.TempDir())
	initTrustMain(t, repo)
	req := &gitCall{}
	req.pinBeforeLocate(repo)
	defer req.closeRemoveHold()
	if req.removePinned || req.removeHold != nil || req.removePin != "" {
		t.Errorf("the pin ran: pinned %v, pin %q", req.removePinned, req.removePin)
	}
}

// swapBaseDuringPin puts a link to second at the name of holder when the trust read of
// the pin starts.
func swapBaseDuringPin(t *testing.T, holder, second string) *bool {
	t.Helper()
	oldHolds := pathHoldsLink
	t.Cleanup(func() { pathHoldsLink = oldHolds })
	moved := new(bool)
	pathHoldsLink = func(p string) bool {
		if !*moved {
			if s := string(debug.Stack()); strings.Contains(s, "pinBeforeLocate") && strings.Contains(s, "requestGitDirTrust") {
				*moved = true
				if err := os.Rename(holder, holder+"old"); err != nil {
					t.Error(err)
				}
				if err := os.Symlink(second, holder); err != nil {
					t.Error(err)
				}
			}
		}
		return oldHolds(p)
	}
	return moved
}

// A real folder holds the first repository. During the pin a link to the second
// repository takes its name, and the folder comes back before the locate. The second
// repository keeps its entry and its ref, for a plain baseRepo and a subfolder.
// Mutations: in pinBeforeLocate, drop the pinIsOfBase test, or keep removePin there.
func TestRemoveOfARealBaseSwappedDuringThePin(t *testing.T) {
	for name, sub := range map[string]string{"plain": "", "subfolder": "sub"} {
		t.Run(name, func(t *testing.T) {
			f := newMovedJunction(t)
			holder := filepath.Join(filepath.Dir(f.P), "holder")
			repo := filepath.Join(holder, "repo")
			initTrustMain(t, repo)
			base := filepath.Join(repo, sub)
			leaf := filepath.Join(base, ".claude", "worktrees", "w1")
			for _, d := range []string{filepath.Dir(leaf), filepath.Join(f.B, sub)} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			runGit(t, repo, "worktree", "add", "-q", "-b", "w1", leaf)
			addW1(t, f.B)
			entryB := filepath.Join(f.B, ".git", "worktrees", "w1")
			writeFile(t, filepath.Join(entryB, "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
			resetUserExcludesCache(t)
			clearDaemonConfigEnv(t)
			moved := swapBaseDuringPin(t, holder, filepath.Dir(f.B))
			atStep(t, "remove pinned", func() {
				if !*moved {
					t.Error("the move did not run")
					return
				}
				if err := os.Remove(holder); err != nil {
					t.Error(err)
				}
				if err := os.Rename(holder+"old", holder); err != nil {
					t.Error(err)
				}
			})
			got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{
				"baseRepo": base, "worktreePath": leaf, "branchName": "w1"}))
			if !exists(filepath.Join(entryB, "HEAD")) {
				t.Errorf("the registration of the second repository went: %s", got)
			}
			if out := gitOut(t, f.B, "for-each-ref", "refs/heads/w1"); out == "" {
				t.Errorf("the ref of the second repository went: %s", got)
			}
		})
	}
}

// The same swap keeps neither a pin nor a hold of the second repository. Mutation: in
// pinBeforeLocate, drop the closeRemoveHold call after pinIsOfBase.
func TestPinOfASwappedBaseKeepsNoHold(t *testing.T) {
	f := newMovedJunction(t)
	holder := filepath.Join(filepath.Dir(f.P), "holder")
	repo := filepath.Join(holder, "repo")
	initTrustMain(t, repo)
	moved := swapBaseDuringPin(t, holder, filepath.Dir(f.B))
	req := &gitCall{}
	req.pinBeforeLocate(repo)
	defer req.closeRemoveHold()
	if !*moved {
		t.Fatal("the move did not run")
	}
	if req.removePin != "" || req.removeHold != nil {
		t.Errorf("pin %q, hold %v", req.removePin, req.removeHold != nil)
	}
}

// A base folder that gives no handle keeps no pin and no hold, so the remove has nothing
// of another repository to act through. Mutation: in pinBeforeLocate, drop the base ==
// nil test.
func TestPinWithNoBaseHandleKeepsNoHold(t *testing.T) {
	f := newMovedJunction(t)
	old := openPinBase
	t.Cleanup(func() { openPinBase = old })
	openPinBase = func(string) *os.Root { return nil }
	if v := requestGitDirTrust(f.A, false); v.verdict != gitDirTrusted || v.pinCommonDir == "" {
		t.Fatalf("the trust check answered %+v", v)
	}
	req := &gitCall{}
	req.pinBeforeLocate(f.A)
	defer req.closeRemoveHold()
	if !req.removePinned || req.removePin != "" || req.removeHold != nil {
		t.Errorf("pinned %v, pin %q, hold %v", req.removePinned, req.removePin, req.removeHold != nil)
	}
}
