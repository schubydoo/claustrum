//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A baseRepo the daemon can open but not search (mode 0600) answers with the
// failed look at .claude, and nothing is deleted. Measured side by side against
// f6010b97 on Linux and macOS VMs.
func TestWorktreeRemoveUnsearchableBaseRepo(t *testing.T) {
	requireGit(t)
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission this test depends on")
	}
	base := filepath.Join(t.TempDir(), "locked-out")
	target := filepath.Join(base, ".claude", "worktrees", "s0")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o755) })

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": base, "worktreePath": target}))
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
		`"failed to remove worktree: statat .claude: permission denied"}}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("worktreePath was deleted: %v", err)
	}
}

// A baseRepo the daemon cannot open at all (mode 0000) answers with the open
// error for baseRepo itself, and nothing is deleted. Measured against f6010b97 on
// Linux and macOS VMs.
func TestWorktreeRemoveUnopenableBaseRepo(t *testing.T) {
	requireGit(t)
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission this test depends on")
	}
	base := filepath.Join(t.TempDir(), "shut")
	target := filepath.Join(base, ".claude", "worktrees", "s0")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o755) })

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": base, "worktreePath": target}))
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
		`"failed to remove worktree: open ` + jsonEscape(t, base) + `: permission denied"}}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("worktreePath was deleted: %v", err)
	}
}

// A .claude that is a symlink, even to a directory outside the repository or to
// nothing, does not stop the removal of a registered worktree outside .claude.
// Measured against f6010b97 on Linux and macOS VMs.
func TestWorktreeRemoveClaudeSymlinkDoesNotBlock(t *testing.T) {
	requireGit(t)
	for _, tc := range []struct{ name, link string }{
		{"outside", ""}, // filled with an absolute path below
		{"relative-outside", "../shared"},
		{"dangling", "/nonexistent/claude"},
		{"loop", ".claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			repo := filepath.Join(dir, "T")
			shared := filepath.Join(dir, "shared")
			if err := os.MkdirAll(shared, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "init", "-q")
			runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
			wt := filepath.Join(repo, "wt")
			runGit(t, repo, "worktree", "add", "-q", wt, "-b", "w")
			link := tc.link
			if link == "" {
				link = shared
			}
			if err := os.Symlink(link, filepath.Join(repo, ".claude")); err != nil {
				t.Fatal(err)
			}

			s := newTestServer(t)
			got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
				map[string]any{"baseRepo": repo, "worktreePath": wt}))
			if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			if _, err := os.Stat(wt); !os.IsNotExist(err) {
				t.Errorf("worktree still present: %v", err)
			}
		})
	}
}

// With worktreeRoot, a search-only baseRepo (mode 0100) is not looked into for
// .claude, so removing an external worktree that is already gone still answers
// success, as on f6010b97 (measured side by side on a Linux VM). Without
// worktreeRoot the same baseRepo answers the open error.
func TestWorktreeRemoveExternalSearchOnlyBaseRepo(t *testing.T) {
	requireGit(t)
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission this test depends on")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "R")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := filepath.Join(base, "mine")
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(repo, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(repo, 0o755) })

	s := newTestServer(t)
	gone := filepath.Join(root, "d", "absent")
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": gone, "worktreeRoot": root}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
		t.Errorf("with worktreeRoot:\n got  %s\n want %s", got, want)
	}
	inRepo := filepath.Join(repo, ".claude", "worktrees", "s0")
	got = dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": repo, "worktreePath": inRepo}))
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
		`"failed to remove worktree: open ` + jsonEscape(t, repo) + `: permission denied"}}`
	if got != want {
		t.Errorf("without worktreeRoot:\n got  %s\n want %s", got, want)
	}
}

// The no-repository refusal applies only without worktreeRoot. With worktreeRoot,
// removing an external worktree that is already gone still answers success when
// baseRepo is a plain directory, as it did before that refusal existed.
func TestWorktreeRemoveExternalNoRepositoryKeepsItsAnswer(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", base)
	plain := filepath.Join(base, "N")
	root := filepath.Join(base, "mine")
	for _, d := range []string{plain, filepath.Join(root, "d")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	s := newTestServer(t)
	gone := filepath.Join(root, "d", "absent")
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": plain, "worktreePath": gone, "worktreeRoot": root}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
