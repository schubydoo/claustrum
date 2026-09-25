package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A baseRepo that is an existing directory with no repository in it cannot have
// its worktree registrations examined, so git.worktree_remove refuses with the
// lock-check text and deletes nothing. Measured against f6010b97 on Linux, macOS
// and Windows VMs, and 90fca6e6 answers the same: the directory is kept, where
// claustrum answered {"success":true} and deleted it.
func TestWorktreeRemoveRefusesWhenBaseRepoHoldsNoRepository(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	// Stop git's upward search at root, so a repository that happens to enclose
	// the temp dir cannot turn "no repository" into "that outer repository".
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	plain := filepath.Join(root, "plain")
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, broken, "init", "-q")
	// A .git without objects/ is not a repository to git.
	if err := os.RemoveAll(filepath.Join(broken, ".git", "objects")); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t)
	for _, base := range []string{plain, broken} {
		target := filepath.Join(base, ".claude", "worktrees", "s0")
		keep := filepath.Join(target, "keep.txt")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keep, []byte("k\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
			map[string]any{"baseRepo": base, "worktreePath": target}))
		want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
			`"failed to remove worktree: could not check whether ` + jsonEscape(t, target) +
			` is locked (its registrations could not be examined); retry"}}`
		if got != want {
			t.Errorf("baseRepo %s:\n got %s\nwant %s", filepath.Base(base), got, want)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("baseRepo %s: worktreePath was deleted: %v", filepath.Base(base), err)
		}
	}
}

// jsonEscape returns s as it appears inside a JSON string.
func jsonEscape(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b[1 : len(b)-1])
}
