package git

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRemoveOnMissingBaseRepoRunsNoStartTest pins a git.worktree_remove on a
// baseRepo that does not exist, as the first requests of a daemon with a run
// folder. The rows of 4c111e86 on Windows have success with branchKept, one
// git call (the excludes read), no start test and no `git version`. A later
// remove makes no call, and the next git.status runs the start test.
func TestRemoveOnMissingBaseRepoRunsNoStartTest(t *testing.T) {
	f := newShadowFixture(t)
	root := resolveTestRoot(t, t.TempDir())
	const want = `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
	bases := []string{filepath.Join(root, "nx", "repo"), filepath.Join(root, "nx2"), filepath.Join(root, "nx", "repo")}
	for i, base := range bases {
		raw, calls := f.call(t, "git.worktree_remove", map[string]any{
			"baseRepo": base, "worktreePath": filepath.Join(base, ".claude", "worktrees", "w1"), "branchName": "w1"})
		if raw != want {
			t.Errorf("remove %d on %s = %s\nwant %s", i, base, raw, want)
		}
		var other []string
		for _, c := range calls {
			if i > 0 || !c.excludesRead() {
				other = append(other, strings.Join(c.argv, " "))
			}
		}
		if len(other) != 0 {
			t.Errorf("remove %d on %s made git calls %q, want none but the first excludes read", i, base, other)
		}
	}
	if names := daemonFolders(t, f.run); names != nil {
		t.Errorf("the removes made git-shadow: %v", names)
	}
	_, calls := f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo})
	if n := len(testCalls(calls)); n == 0 {
		t.Errorf("the git.status after the removes ran no start test, calls %d", len(calls))
	}
}
