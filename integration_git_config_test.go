package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSocketGitConfigRefusal pins the frame of each of the five git methods for
// a repository config that the daemon refuses: format version 2. 5fd08069
// answers these frames for git.info and git.status (measured on Linux and
// Windows) and for git.worktree_create and git.worktree_remove (measured on
// Linux). 5fd08069 answers the frame of git.list_branches (measured on Linux).
//
// The detail of this refusal holds no path, so the golden is the same on each
// system. The worktree path of the remove text and the root both read <DIR>.
func TestSocketGitConfigRefusal(t *testing.T) {
	// A repository with one commit and the linked worktree wr, built here with
	// runGit. Its config then holds format version 2.
	requireGit(t)
	root := resolveTestRoot(t, t.TempDir())
	f := struct{ repo, wt string }{filepath.Join(root, "repo"), filepath.Join(root, "repo", ".claude", "worktrees", "wr")}
	runGit(t, root, "init", "-q", "-b", "main", "repo")
	runGit(t, f.repo, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, f.repo, "worktree", "add", "-q", f.wt, "-b", "wr")
	config := "[core]\n\trepositoryformatversion = 2\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n"
	if err := os.WriteFile(filepath.Join(f.repo, ".git", "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	sock := startSocketServer(t)
	cl := dial(t, sock)
	calls := []string{
		req(1, "git.info", map[string]any{"path": f.repo}),
		req(2, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo}),
		req(3, "git.list_branches", map[string]any{"path": f.repo}),
		req(4, "git.worktree_create", map[string]any{
			"baseRepo": f.repo, "branchName": "w1", "worktreePath": filepath.Join(f.repo, ".claude", "worktrees", "w1")}),
		req(5, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.wt}),
	}
	got := make([]json.RawMessage, len(calls))
	for i, line := range calls {
		got[i] = normPath(normPath(cl.call(line), f.wt), root)
	}
	assertGolden(t, "socket_git_config_refusal.golden.json", encodeGolden(t, got))
}
