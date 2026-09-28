package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The git.worktree_remove tests that need no POSIX fixture, so the Windows leg runs
// them too.

// lockedWorktree builds a repo with a LOCKED worktree.
func lockedWorktree(t *testing.T) (repo, wt string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := resolveTestRoot(t, t.TempDir())
	repo = filepath.Join(base, "repo")
	// 7d193f89 confines worktrees to inside the repo, so the holder is under
	// .claude/worktrees.
	holder := filepath.Join(repo, ".claude", "worktrees")
	wt = filepath.Join(holder, "wt")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(holder, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		cmd.Env = append(cmd.Env, gitNoAutoMaintenance...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("worktree", "add", "-q", "-b", "wtb", wt)
	run("worktree", "lock", wt)
	return repo, wt
}

// 7d193f89 REFUSES a locked worktree removal: success:false with a fixed message,
// leaving the directory in place. Before 7d193f89
// the reference DELETED it and answered success:true; that older behavior is the
// wire divergence this reconciles. Measured against 7d193f89 on an ephemeral VM,
// both binaries returning byte-identical frames.
func TestWorktreeRemoveLockedWorktreeIsRefused(t *testing.T) {
	repo, wt := lockedWorktree(t)
	s := newTestServer(t)

	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": wt}))
	const want = "is locked (git worktree lock); unlock it to remove it"
	if !strings.Contains(raw, `"success":false`) || !strings.Contains(raw, want) {
		t.Errorf("reply = %s, want success:false with the locked refusal", raw)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the locked worktree was deleted (%v); 7d193f89 leaves it in place", err)
	}
}

// An ordinary non-worktree directory inside the repo is deleted and answers
// success:true. 7d193f89 and f6010b97 do the same (measured on VMs, row K05). Only a
// locked registration is refused.
func TestWorktreeRemoveDeletesNonWorktreeDir(t *testing.T) {
	requireGit(t)
	repo := resolveTestRoot(t, t.TempDir())
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	nd := filepath.Join(repo, ".claude", "worktrees", "nd") // never a worktree
	writeFile(t, filepath.Join(nd, "KEEP.txt"), "x\n", 0o600)

	s := newTestServer(t)
	raw := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": nd}))
	if !strings.Contains(raw, `"success":true`) {
		t.Errorf("reply = %s, want success:true", raw)
	}
	if _, err := os.Stat(nd); err == nil {
		t.Error("the non-worktree directory survived")
	}
}
