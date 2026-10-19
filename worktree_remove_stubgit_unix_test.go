//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The locked refusal reads the `locked` marker file (7d193f89 runs no `git worktree
// remove` at all), not git's localised stderr, so it holds
// under any locale. Proven with a stub git that EXITS 0 on every call: only the
// marker-file check stands between the caller and a deletion, so a refusal here means
// the file-based path fired, not git's output. Without that check the stub "succeeds"
// and the remove answers success:true.
func TestWorktreeRemoveLockedByMarkerNotStderr(t *testing.T) {
	repo, wt := lockedWorktree(t) // real git creates the worktree + the `locked` marker
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wt}))
	if !strings.Contains(raw, "is locked (git worktree lock); unlock it to remove it") {
		t.Errorf("reply = %s, want the marker-file locked refusal (stub git exits 0, so only the file check can refuse)", raw)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("locked worktree deleted (%v); the marker-file check must leave it in place", err)
	}
}

// The daemon drops the entry itself after it deletes the tree. A real linked worktree
// with its `locked` marker deleted (so the refusal above does not fire) plus a stub git
// that exits 0 without doing anything shows that no git call drops it. Without the
// drop a re-create at the same path would fail "already registered".
func TestWorktreeRemovePrunesLeftoverAdminDir(t *testing.T) {
	repo, wt := lockedWorktree(t) // real git builds the registration
	admin := filepath.Join(repo, ".git", "worktrees", "wt")
	if err := os.Remove(filepath.Join(admin, "locked")); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wt}))
	if !strings.Contains(raw, `"success":true`) {
		t.Errorf("reply = %s, want success:true", raw)
	}
	if _, err := os.Stat(admin); err == nil {
		t.Errorf("the registration at %s survived; the entry drop did not run", admin)
	}
}

// 7d193f89 refuses a worktree whose path crosses a symlinked component under the
// repo (.claude / .claude/worktrees or below) — a planted link could carry a
// create outside the repo or point the removal at an external target. Create carries errorCode "symlinked_component"; remove carries
// none. Measured against the reference on an ephemeral VM.
func TestWorktreeSymlinkedComponentRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		cmd.Env = append(cmd.Env, gitNoAutoMaintenance...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "-q", "--allow-empty", "-m", "init")
	// .claude/worktrees is a symlink escaping the repo — a planted link.
	if err := os.Symlink(base, filepath.Join(repo, ".claude", "worktrees")); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(repo, ".claude", "worktrees", "wt")
	const want = "is a symbolic link; a symlinked .claude or .claude/worktrees inside the " +
		"repository is not supported for SSH sessions, because a repository can plant such a " +
		"link and a planted one cannot reliably be told apart from your own. Replace it with a " +
		"real directory (or delete it and it will be recreated)"

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": repo, "branchName": "b", "worktreePath": wt}))
	if !strings.Contains(got, want) || !strings.Contains(got, `"errorCode":"symlinked_component"`) {
		t.Errorf("create = %s, want the symlinked_component refusal", got)
	}
	got = dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wt}))
	if !strings.Contains(got, want) || strings.Contains(got, `"errorCode"`) {
		t.Errorf("remove = %s, want the symlinked_component refusal with no errorCode", got)
	}
}
