//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// git.worktree_remove's wipesHomeDir guard is defense-in-depth here: it fires only
// when a repo is an ancestor of home, so a worktreePath that passes the containment
// check still IS the home directory. With HOME under baseRepo, worktreePath=home
// reaches the guard and is refused before any delete. os.UserHomeDir reads $HOME on
// unix, so this is unix-scoped.
func TestWorktreeRemoveRefusesHomeUnderRepo(t *testing.T) {
	repo := t.TempDir()
	home := filepath.Join(repo, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": home}))
	if !strings.Contains(got, "must not be or contain the home directory") {
		t.Errorf("worktree_remove of home = %s, want the home-dir refusal", got)
	}
}
