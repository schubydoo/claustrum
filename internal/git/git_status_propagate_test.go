package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// git.status on a genuine session worktree whose `git status` itself fails (a
// corrupt per-worktree index) propagates the git error as -32603. The worktree-of
// check runs rev-parse, which never reads the index, so it still passes; only
// status trips.
func TestGitStatusPropagatesGitFailure(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(repo, ".claude", "worktrees", "w")
	runGit(t, repo, "worktree", "add", "-q", wt, "-b", "w")
	index := filepath.Join(repo, ".git", "worktrees", "w", "index")
	if err := os.WriteFile(index, []byte("not an index"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The fixture must make git itself fail, or the assertion below would be
	// checking the wrong thing.
	if _, err := hardenedGitStdout(gitUse{}, wt, true, "status", "--porcelain"); err == nil {
		t.Skip("this git build tolerates a corrupt worktree index")
	}
	raw := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.status", map[string]any{"path": wt, "baseRepo": repo}))
	if !strings.Contains(raw, `"code":-32603`) {
		t.Errorf("git.status with a corrupt index = %s, want -32603", raw)
	}
}
