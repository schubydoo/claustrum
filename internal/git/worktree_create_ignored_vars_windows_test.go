package git

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestIgnoredCommonDirCreateRegistersInBaseRepo: under a daemon GIT_COMMON_DIR of
// a clone O of the repository R, a create in R registers the new worktree in R,
// and the remove deletes that entry. O does not change. 87375cc7 does so on
// Windows (measured on Windows). On Linux and macOS the add is staged with the
// variable too (worktreeAdd), so only Windows runs this test. Mutation:
// daemonCommonDirSet returns daemonCommonDirInEnv.
func TestIgnoredCommonDirCreateRegistersInBaseRepo(t *testing.T) {
	f := newShadowFixture(t)
	setAttrSource(t, true)
	repo, gitDir := configRepo(t)
	o := filepath.Join(filepath.Dir(repo), "o")
	runGit(t, filepath.Dir(repo), "clone", "-q", repo, o)
	before := treeState(t, gitDirOf(o))
	t.Setenv("GIT_COMMON_DIR", gitDirOf(o))
	leaf := filepath.Join(repo, ".claude", "worktrees", "w2")
	if raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": repo, "branchName": "w2", "worktreePath": leaf}); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("create = %s", raw)
	}
	if !exists(filepath.Join(gitDir, "worktrees", "w2")) {
		t.Error("the entry w2 is not in R")
	}
	if got := treeState(t, gitDirOf(o)); got != before {
		t.Errorf("the create changed O:\n%s\n---\n%s", before, got)
	}
	if raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": repo, "worktreePath": leaf, "branchName": "w2"}); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("remove = %s", raw)
	}
	if exists(filepath.Join(gitDir, "worktrees", "w2")) || exists(leaf) {
		t.Error("the remove kept the entry w2 or its folder")
	}
	if got := treeState(t, gitDirOf(o)); got != before {
		t.Errorf("the remove changed O:\n%s\n---\n%s", before, got)
	}
}
