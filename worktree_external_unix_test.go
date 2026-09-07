//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// The uid-ownership refusal is the first check in the reference's order and needs a
// root the daemon user does not own — the filesystem root is the one such directory
// present on every unix host. worktreeRootShareRefusal only stats, so this touches
// nothing. The ownership is asserted first so a host where "/" is somehow ours skips
// rather than reporting a false negative.
func TestWorktreeRootShareRefusalForeignOwner(t *testing.T) {
	skipIfRoot(t)
	root := string(os.PathSeparator)
	fi, err := os.Stat(root)
	if err != nil {
		t.Skipf("cannot stat %s: %v", root, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == os.Geteuid() {
		t.Skipf("%s is owned by the test user; the foreign-owner arm is unreachable here", root)
	}
	want := fmt.Sprintf("is owned by uid %d, not by you (uid %d)", st.Uid, os.Geteuid())
	if got := worktreeRootShareRefusal(root); !strings.Contains(got, want) {
		t.Errorf("worktreeRootShareRefusal(%s) = %q, want it to contain %q", root, got, want)
	}
}

// The symlinked-<directory> refusal gates REMOVE as well as create (the create arm
// is covered by TestWorktreeCreateExternalSpellingAndSymlink): a planted link at the
// <directory> level must not carry the removal out of the worktree location.
func TestWorktreeRemoveExternalDirSymlink(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	root := filepath.Join(base, "mine")
	outside := filepath.Join(base, "outside", "wt")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "outside"), filepath.Join(root, "dlink")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": filepath.Join(root, "dlink", "wt"), "worktreeRoot": root}))
	if !strings.Contains(worktreeErrorField(t, raw), "is a symbolic link; the directory under the worktree location must be a real directory") {
		t.Errorf("remove through a symlinked <dir> = %s, want the symlink refusal", raw)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the removal followed the symlink out of the root (%v)", err)
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
