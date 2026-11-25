//go:build unix

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A remove that keeps no pin of the git directory deletes its entry by the path that
// git answered. A junction in the path of baseRepo that moves to a second repository
// during the tree delete keeps the entry of both repositories (dropWorktreeEntry,
// claustrum's own, not measured). The branch step still takes the ref of the same name
// in the second repository, so the reply has no branchKept. The pin fails here
// (failThePin). Mutations: drop the
// moved test of dropWorktreeEntry, or pass nil for it (the locked entry of the second
// repository goes).
func TestRemoveWithNoPinKeepsTheEntryOfAMovedJunction(t *testing.T) {
	f := newMovedJunction(t)
	for _, repo := range []string{f.A, f.B} {
		runGit(t, repo, "worktree", "add", "-q", "-b", "w1", filepath.Join(repo, ".claude", "worktrees", "w1"))
	}
	runGit(t, f.B, "worktree", "lock", filepath.Join(f.B, ".claude", "worktrees", "w1"))
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	failThePin(t, f.A)
	moved := f.moveAtLeafOpen(t)
	got := f.removeW1(t)
	if !*moved {
		t.Fatalf("the tree delete did not run: %s", got)
	}
	text := "removed the worktree but could not drop its registration (baseRepo leads to another folder now)"
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, text) + `}}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if out := gitOut(t, f.B, "for-each-ref", "refs/heads/w1"); out != "" {
		t.Error("the branch step kept the ref of the same name in the second repository")
	}
	if out := gitOut(t, f.A, "for-each-ref", "refs/heads/w1"); out == "" {
		t.Error("the branch of the first repository went")
	}
	for _, p := range []string{"HEAD", "locked"} {
		if !exists(filepath.Join(f.B, ".git", "worktrees", "w1", p)) {
			t.Errorf("the entry file %s of the second repository went", p)
		}
	}
	if !exists(filepath.Join(f.A, ".git", "worktrees", "w1", "HEAD")) {
		t.Error("the entry of the first repository went through the moved path")
	}
}

// The same move for a live leaf whose `.git` names no entry, so the entry goes by its
// record (dropWorktreeEntryByPath). The entry of the second repository names the leaf
// as the caller spelled it. It stays, and the reply has no text of the kept entry
// (claustrum's own, not measured). Mutation: pass nil for the moved test of
// dropWorktreeEntryByPath in gitWorktreeRemoveLocked (the entry goes).
func TestRemoveByRecordWithNoPinKeepsTheEntryOfAMovedJunction(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.A, f.B)
	spelledLeaf := filepath.Join(f.P, "repo", ".claude", "worktrees", "w1")
	writeFile(t, filepath.Join(f.A, ".claude", "worktrees", "w1", ".git"), "gitdir: "+filepath.Join(f.A, ".git", "worktrees", "other")+"\n", 0o644)
	writeFile(t, filepath.Join(f.B, ".git", "worktrees", "w1", "gitdir"), filepath.Join(spelledLeaf, ".git")+"\n", 0o644)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	failThePin(t, f.A)
	moved := f.moveAtLeafOpen(t)
	got := f.removeW1(t)
	if !*moved {
		t.Fatalf("the tree delete did not run: %s", got)
	}
	if strings.Contains(got, "could not drop its registration") {
		t.Errorf("reply %s, want no text of the kept entry", got)
	}
	if !exists(filepath.Join(f.B, ".git", "worktrees", "w1", "HEAD")) {
		t.Errorf("the entry of the second repository went: %s", got)
	}
	if !exists(filepath.Join(f.A, ".git", "worktrees", "w1", "HEAD")) {
		t.Error("the entry of the first repository went through the moved path")
	}
}

// A gone leaf with no pin. The junction moves at the pair of the probe, and the
// second repository has an entry whose record names the leaf as the caller spelled
// it. Both entries stay (dropWorktreeEntryByPath, claustrum's own, not measured).
// Mutations: drop the moved test of dropWorktreeEntryByPath, or pass nil for it in
// removeGoneWorktree (the entry of the second repository goes).
func TestGoneRemoveWithNoPinKeepsTheEntryOfAMovedJunction(t *testing.T) {
	f := newMovedJunction(t)
	spelled := filepath.Join(f.P, "repo")
	spelledLeaf := filepath.Join(spelled, ".claude", "worktrees", "w1")
	runGit(t, f.A, "worktree", "add", "-q", "-b", "other", filepath.Join(f.A, "otherwt"))
	runGit(t, f.B, "worktree", "add", "-q", "-b", "w1", filepath.Join(f.B, ".claude", "worktrees", "w1"))
	writeFile(t, filepath.Join(f.B, ".git", "worktrees", "w1", "gitdir"), filepath.Join(spelledLeaf, ".git")+"\n", 0o644)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	failThePin(t, f.A)
	installGitSlowStub(t, realGit)
	stubStepsNow(t, "rev-parse,--show-toplevel", f.moveSteps()...)
	got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{
		"baseRepo": spelled, "worktreePath": spelledLeaf, "branchName": "w1"}))
	if !exists(f.P + "old") {
		t.Fatalf("the junction did not move: %s", got)
	}
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !exists(filepath.Join(f.B, ".git", "worktrees", "w1", "HEAD")) {
		t.Errorf("the entry of the second repository went: %s", got)
	}
	if !exists(filepath.Join(f.A, ".git", "worktrees", "otherwt", "HEAD")) || !exists(filepath.Join(f.A, "otherwt", ".git")) {
		t.Error("the other worktree of the first repository went")
	}
	if out := gitOut(t, f.A, "for-each-ref", "refs/heads/other"); out == "" {
		t.Error("the other branch of the first repository went")
	}
}

// With a daemon GIT_COMMON_DIR outside baseRepo, the registrations do not move with the
// junction. A move during the tree delete still deletes the entry in that git
// directory, as before this guard, and the second repository keeps its entry
// (gitDirBelowBase). The pin fails, so no hold takes the delete. Mutation: guard every entry delete with no hold by the move test
// (the entry in the git directory of the daemon stays).
func TestRemoveWithADaemonCommonDirAndAMovedJunction(t *testing.T) {
	f := newMovedJunction(t)
	addW1(t, f.B)
	z := f.separateGitDir(t)
	resetUserExcludesCache(t)
	clearDaemonConfigEnv(t)
	failThePin(t, f.A)
	moved := f.moveAtLeafOpen(t)
	got := f.removeW1(t)
	if !*moved {
		t.Fatalf("the tree delete did not run: %s", got)
	}
	if exists(filepath.Join(z, "worktrees", "w1")) {
		t.Errorf("the entry in the git directory of the daemon stayed: %s", got)
	}
	if exists(filepath.Join(f.A, ".claude", "worktrees", "w1")) {
		t.Error("the leaf of the first repository stayed")
	}
	if !exists(filepath.Join(f.B, ".git", "worktrees", "w1", "HEAD")) {
		t.Error("the entry of the second repository went")
	}
}

// moveAtLeafOpen moves the junction to the second repository once, when the tree
// delete opens the leaf. The answer reports the move.
func (f movedJunction) moveAtLeafOpen(t *testing.T) *bool {
	t.Helper()
	oldLeaf := leafOpenedHook
	t.Cleanup(func() { leafOpenedHook = oldLeaf })
	moved := false
	leafOpenedHook = func() {
		if !moved {
			moved = true
			replaceLinkForTest(t, f.P, filepath.Dir(f.B))
		}
	}
	return &moved
}

// failThePin makes the pin of a remove in repo keep no git directory: `.git`
// names a gone folder while the pin reads it, and the real folder is back right
// after the pin.
func failThePin(t *testing.T, repo string) {
	t.Helper()
	dotGit := filepath.Join(repo, ".git")
	oldOpen, oldStep := openPinBase, createStepHook
	t.Cleanup(func() { openPinBase, createStepHook = oldOpen, oldStep })
	openPinBase = func(at string) *os.Root {
		r := oldOpen(at)
		mustDo(t, os.Rename(dotGit, dotGit+".real"))
		mustDo(t, os.WriteFile(dotGit, []byte("gitdir: "+dotGit+".gone\n"), 0o644))
		return r
	}
	createStepHook = func(step string) {
		if step == "remove pinned" {
			mustDo(t, os.Remove(dotGit))
			mustDo(t, os.Rename(dotGit+".real", dotGit))
		}
	}
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func replaceLinkForTest(t *testing.T, link, to string) {
	t.Helper()
	mustDo(t, os.Remove(link))
	mustDo(t, os.Symlink(to, link))
}
