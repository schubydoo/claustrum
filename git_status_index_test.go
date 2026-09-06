package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGitStatusDoesNotRewriteIndex pins the GIT_OPTIONAL_LOCKS=0 parity (4534d86): a
// plain `git status` refreshes the worktree's index (rewriting the stat cache and
// taking index.lock) — a mutation of the caller's repo on a read. The reference sets
// GIT_OPTIONAL_LOCKS=0 so status leaves the index untouched, with byte-identical
// output. This makes the stat cache stale (touch a tracked file, unchanged content)
// so a lock-taking status WOULD rewrite the index, then asserts the index bytes are
// unchanged after git.status. Reverting the env flips it: status rewrites the index.
//
// Cross-platform on purpose (no build tag): the fix has no OS branch, so the Windows
// leg must exercise it too. It uses only os/filepath/bytes and the socket harness —
// no POSIX shell (unlike the .gitattributes-filter sibling, which stays //go:build unix).
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

	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.call(req(1, "git.status", map[string]any{"path": wt, "baseRepo": repo}))

	after, err := os.ReadFile(idx)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("git.status rewrote the worktree index (mutation on read); GIT_OPTIONAL_LOCKS=0 must prevent it")
	}
}
