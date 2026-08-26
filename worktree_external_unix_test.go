//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An external worktreeRoot writable by every user on the host is refused (mode is
// echoed). The uid-ownership and shared-group refusals need a foreign owner / a
// shared group and are VM-verified rather than exercised here. Measured against
// 7d193f89. The asserted substring is the suffix common to the world-only and
// group+world spellings, so a temp dir whose gid differs from the process egid
// (which would add "its group and ") does not make the test flaky.
func TestWorktreeCreateExternalWorldWritable(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := filepath.Join(base, "wld")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "w",
			"worktreePath": filepath.Join(root, "d", "wt"), "worktreeRoot": root}))
	if !strings.Contains(raw, `"errorCode":"unsafe_path"`) ||
		!strings.Contains(raw, "every user on this host (mode 0777)") {
		t.Errorf("create with a world-writable root = %s, want the writable refusal", raw)
	}
}
