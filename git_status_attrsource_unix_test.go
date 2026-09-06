//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGitStatusIgnoresInRepoGitattributes pins the --attr-source=<empty-tree> parity
// (4534d86): git.status must NOT honor a repository's in-repo .gitattributes. A
// clean filter is assigned to `data`; the filter rewrites aaaa->bbbb (same length, so
// git cannot decide modified-ness by size and must run the filter when the attribute
// is active). The index holds the cleaned blob (bbbb); the worktree keeps the raw
// aaaa.
//
//   - with --attr-source (correct): the .gitattributes is ignored, `data` has no
//     filter, raw aaaa != index bbbb -> reported MODIFIED (" M data"), and the clean
//     command never runs.
//   - without it (the bug): git runs the clean filter, clean(aaaa)=bbbb == index ->
//     reported CLEAN, and an attacker-controlled filter command executes.
//
// So this asserts MODIFIED and a sentinel-not-written: reverting the --attr-source
// arg flips both.
func TestGitStatusIgnoresInRepoGitattributes(t *testing.T) {
	requireGit(t)
	if len(attrSourceArgs()) == 0 {
		t.Skip("git does not support --attr-source (added in 2.40)")
	}
	root := resolveTestRoot(t, t.TempDir())
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "main", "repo")
	runGit(t, repo, "config", "user.email", "t@t")
	runGit(t, repo, "config", "user.name", "t")

	// A clean filter that records that it ran (sentinel) and rewrites aaaa->bbbb.
	sentinel := filepath.Join(root, "filter-ran")
	runGit(t, repo, "config", "filter.redact.clean",
		"sh -c 'printf ran > \""+sentinel+"\"; exec tr a b'")
	writeFile(t, filepath.Join(repo, "data"), "aaaa\n", 0o644)
	writeFile(t, filepath.Join(repo, ".gitattributes"), "data filter=redact\n", 0o644)
	runGit(t, repo, "add", "data", ".gitattributes") // index blob becomes bbbb
	runGit(t, repo, "commit", "-m", "seed")
	_ = os.Remove(sentinel) // clear the add-time run; only status-time matters

	wt := filepath.Join(repo, ".claude", "worktrees", "wt")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "worktree", "add", "-q", "--detach", wt)
	writeFile(t, filepath.Join(wt, "data"), "aaaa\n", 0o644) // raw, same size as bbbb

	sock := startSocketServer(t)
	cl := dial(t, sock)
	var resp struct {
		Result struct {
			IsRepo  bool     `json:"isRepo"`
			Clean   bool     `json:"clean"`
			Changes []string `json:"changes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(cl.call(req(1, "git.status", map[string]any{"path": wt, "baseRepo": repo})), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Result.Clean || len(resp.Result.Changes) != 1 || resp.Result.Changes[0] != " M data" {
		t.Errorf("git.status honored the in-repo .gitattributes: got clean=%v changes=%v, want clean=false changes=[\" M data\"] (--attr-source not applied?)",
			resp.Result.Clean, resp.Result.Changes)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("the .gitattributes clean filter RAN during git.status; --attr-source must suppress it")
	}
}

// TestGitStatusDoesNotRewriteIndex pins the GIT_OPTIONAL_LOCKS=0 parity (4534d86): a
// plain `git status` refreshes the worktree's index (rewriting the stat cache and
// taking index.lock) — a mutation of the caller's repo on a read. The reference sets
// GIT_OPTIONAL_LOCKS=0 so status leaves the index untouched, with byte-identical
// output. This makes the stat cache stale (touch a tracked file, unchanged content)
// so a lock-taking status WOULD rewrite the index, then asserts the index bytes are
// unchanged after git.status. Reverting the env flips it: status rewrites the index.
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
