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

// TestRemoveOnMissingBaseRepoWithDaemonGitDir pins the same removes with a daemon
// GIT_DIR, to the git directory of a repository and to a missing path. They keep
// the steps of origin/main before this rule: the first remove runs the start test
// and `git version`, a later one `git version` alone, and the reply is success
// with branchKept (claustrum's own, not measured).
func TestRemoveOnMissingBaseRepoWithDaemonGitDir(t *testing.T) {
	const want = `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
	for _, form := range []string{"repository", "missing"} {
		t.Run(form, func(t *testing.T) {
			f := newShadowFixture(t)
			root := resolveTestRoot(t, t.TempDir())
			gitDir := filepath.Join(f.repo, ".git")
			if form == "missing" {
				gitDir = filepath.Join(root, "nogit")
			}
			t.Setenv("GIT_DIR", gitDir)
			base := filepath.Join(root, "nx", "repo")
			for i := range 2 {
				raw, calls := f.call(t, "git.worktree_remove", map[string]any{
					"baseRepo": base, "worktreePath": filepath.Join(base, ".claude", "worktrees", "w1"), "branchName": "w1"})
				if raw != want {
					t.Errorf("remove %d = %s\nwant %s", i, raw, want)
				}
				if n := len(testCalls(calls)); (i == 0) != (n > 0) {
					t.Errorf("remove %d ran %d calls of the start test", i, n)
				}
				if len(calls) == 0 || strings.Join(calls[len(calls)-1].argv, " ") != "version" {
					t.Errorf("remove %d made %d calls, want `git version` last", i, len(calls))
				}
			}
		})
	}
}
