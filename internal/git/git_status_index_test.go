package git

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGitStatusDoesNotRewriteIndex pins that git.status leaves the index of the
// worktree untouched. It makes the stat cache stale (touch a tracked file,
// unchanged content), then asserts the index bytes are unchanged after git.status.
//
// This test does not guard the GIT_OPTIONAL_LOCKS=0 arm. The status command runs
// with --git-dir=<temp>, which holds a copy of the index, so the real index stays
// with the arm removed too. On every system the subtest
// TestHardenedGitCallShape/status (githarden_shape_test.go) fails without the arm.
// On Linux and macOS TestGitStatusCallOrder and TestGitStatusGateCalls
// (git_status_calls_unix_test.go) fail too.
//
// No build tag: it uses no POSIX shell, so it runs on Windows too.
func TestGitStatusDoesNotRewriteIndex(t *testing.T) {
	requireGit(t)
	root := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "main", "repo")
	runGit(t, repo, "config", "user.email", "t@t")
	runGit(t, repo, "config", "user.name", "t")
	writeFile(t, filepath.Join(repo, "f"), "one\n", 0o644)
	runGit(t, repo, "add", "f")
	runGit(t, repo, "commit", "-m", "seed")
	wt := filepath.Join(repo, ".claude", "worktrees", "wt")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "worktree", "add", "-q", "--detach", wt)

	// The linked worktree's index lives at <repo>/.git/worktrees/wt/index. Make the
	// stat cache stale (mtime bump, unchanged content) so a plain status would rewrite it.
	idx := filepath.Join(repo, ".git", "worktrees", "wt", "index")
	future := time.Now().Add(48 * time.Hour)
	if err := os.Chtimes(filepath.Join(wt, "f"), future, future); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(idx)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	d, _ := newShadowDeps(t)
	gitDispatch(t, d, req(1, "git.status", map[string]any{"path": wt, "baseRepo": repo}))

	after, err := os.ReadFile(idx)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("git.status changed the index of the worktree, want it unchanged")
	}
}
