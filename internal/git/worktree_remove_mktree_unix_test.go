//go:build unix

package git

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A remove with worktreeRoot whose start test gets "garbage" from mktree, with
// exit 0, deletes nothing. b3081b51 and be1c1ca5 answer the failed commit-tree
// call to both requests, and the leaf, the registration and the branch stay
// (measured on Linux). Every path of the fixture is under t.TempDir().
func TestRemoveDeletesNothingAfterAMktreeGarbageAnswer(t *testing.T) {
	f := newCellFixture(t)
	root := filepath.Join(f.home, "ext")
	mkdirForTest(t, root)
	leaf := filepath.Join(root, "cp", "w1")
	params := map[string]any{"baseRepo": f.repo, "worktreePath": leaf, "branchName": "w1", "worktreeRoot": root}
	raw, _ := f.call(t, "git.worktree_create", params)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("the create of the worktree = %s", raw)
	}
	// The next request runs the start test again.
	h := f.d.Shadow
	h.mu.Lock()
	h.state = ""
	h.mu.Unlock()
	stageGit(t,
		stagedCall{Match: []string{"mktree"}, Stdout: "garbage\n"},
		stagedCall{Match: []string{"version"}, Stdout: "git version 2.43.0\n"})
	for i := range 2 {
		raw, _ = f.call(t, "git.worktree_remove", params)
		dir := daemonFolder(t, f.run)
		text := "failed to remove worktree: cannot determine the repository's work tree: " +
			failedTestText("git commit-tree failed under "+strconv.Quote(dir)+": fatal: not a valid object name garbage (git version 2.43.0)")
		wantFrame(t, raw, frame(`{"success":false,"error":`+jsonQuoted(t, text)+`}`))
		wantPaths(t, true, filepath.Join(leaf, "README"), filepath.Join(leaf, ".git"),
			filepath.Join(f.gitDir, "worktrees", "w1", "gitdir"), filepath.Join(f.gitDir, "refs", "heads", "w1"))
		if t.Failed() {
			t.Fatalf("request %d", i+1)
		}
	}
}
