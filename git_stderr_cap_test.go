package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundedStderrHead(t *testing.T) {
	if got := boundedStderrHead("  trimmed  "); got != "trimmed" {
		t.Errorf("boundedStderrHead(whitespace) = %q, want %q", got, "trimmed")
	}
	if got := boundedStderrHead(strings.Repeat("x", 1000)); len(got) != stderrHeadCap {
		t.Errorf("boundedStderrHead(1000 bytes) len = %d, want %d", len(got), stderrHeadCap)
	}
	if got := boundedStderrHead("short"); got != "short" {
		t.Errorf("boundedStderrHead(short) = %q, want unchanged", got)
	}
}

// 7d193f89 caps git.worktree_create's failure stderr at 512 bytes (its bounded
// stderr sink), so a long git error does not balloon the frame. A 600-char invalid
// branch name makes `git worktree add` fail with >512 bytes of stderr; the reference
// and claustrum both answer "git worktree add failed: " + a 512-byte head (total
// 537). Measured byte-identical against 7d193f89 on an ephemeral VM.
func TestWorktreeCreateCapsGitStderr(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	long := strings.Repeat("b/", 300) // 600 chars, an invalid branch name

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": long, "worktreePath": filepath.Join(repo, ".claude", "worktrees", "w")}))
	if !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
		t.Fatalf("worktree_create = %s, want worktree_add_failed", raw)
	}
	var resp struct {
		Result struct {
			Error string `json:"error"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	const prefix = "git worktree add failed: "
	if got, want := len(resp.Result.Error), len(prefix)+stderrHeadCap; got != want {
		t.Errorf("error length = %d, want %d (prefix + 512-byte cap); error=%q", got, want, resp.Result.Error)
	}
}
