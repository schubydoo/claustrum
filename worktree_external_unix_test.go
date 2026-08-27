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

// writableWho names who beyond the owner can write. The three spellings are the
// reference's; the combined case is not reachable in the RPC test above without a
// shared group AND world-write, so it is pinned directly.
func TestWritableWho(t *testing.T) {
	cases := []struct {
		group, world bool
		want         string
	}{
		{true, true, "its group and every user on this host"},
		{false, true, "every user on this host"},
		{true, false, "its group"},
		{false, false, ""},
	}
	for _, c := range cases {
		if got := writableWho(c.group, c.world); got != c.want {
			t.Errorf("writableWho(group=%v,world=%v) = %q, want %q", c.group, c.world, got, c.want)
		}
	}
}

// worktreeRootShareRefusal returns "" for a missing root — the create fails later at
// the parent-creation step, not here.
func TestWorktreeRootShareRefusalMissingRoot(t *testing.T) {
	if got := worktreeRootShareRefusal(filepath.Join(t.TempDir(), "nonexistent")); got != "" {
		t.Errorf("missing root = %q, want no refusal", got)
	}
}

// A `.git` file that stats as regular but cannot be read is refused with the same
// "does not name a git dir" reason as garbage content — the fallback never deletes
// a path whose gitdir pointer it could not examine.
func TestExternalWorktreeVerifyUnreadableGitFile(t *testing.T) {
	skipIfRoot(t)
	base := t.TempDir()
	wp := filepath.Join(base, "wt")
	if err := os.Mkdir(wp, 0o755); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(wp, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /nope\n"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitFile, 0o600) })
	reason, transient := externalWorktreeVerify(filepath.Join(base, "repo"), wp)
	if transient || !strings.Contains(reason, "does not name a git dir") {
		t.Errorf("externalWorktreeVerify(unreadable .git) = (%q, %v), want a non-transient \"does not name a git dir\" refusal", reason, transient)
	}
	if _, err := os.Stat(wp); err != nil {
		t.Errorf("verify must not touch the path: %v", err)
	}
}
