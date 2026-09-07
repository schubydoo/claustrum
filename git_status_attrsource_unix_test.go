//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
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

// resetAttrSourceForTest clears the once-cached --attr-source probe so it re-runs
// under a controlled PATH. Direct assignment is safe here because no test in this
// package calls t.Parallel(), so nothing else is probing concurrently.
func resetAttrSourceForTest() {
	attrSourceOnce = sync.Once{}
	attrSourceOK = false
}

// git added --attr-source in 2.40, so the option is probed with the reference's own
// guard (`git --attr-source=<empty> version`) and simply omitted when that guard
// fails. A stub git rejecting the option stands in for an older git. The cache is
// reset on both sides so the real probe is not poisoned for the rest of the run.
func TestAttrSourceArgsAbsentWhenGitRejectsIt(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"),
		[]byte("#!/bin/sh\ncase \"$*\" in *--attr-source*) exit 129 ;; *) exit 0 ;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	resetAttrSourceForTest()
	t.Cleanup(resetAttrSourceForTest)

	if got := attrSourceArgs(); got != nil {
		t.Errorf("attrSourceArgs() = %v, want nil when git rejects --attr-source", got)
	}
}
