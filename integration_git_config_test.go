package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestSocketGitConfigRefusal pins the frame of each of the five git methods for
// a repository config that the daemon refuses: format version 2. 5fd08069
// answers these frames for git.info and git.status (cell C2, Linux and Windows
// VMs) and for git.worktree_create and git.worktree_remove (cell W7, Linux VM).
// 5fd08069 answers the frame of git.list_branches in cell X1 (Linux VM).
//
// The detail of this refusal holds no path, so the golden is the same on each
// system. The worktree path of the remove text and the root both read <DIR>.
func TestSocketGitConfigRefusal(t *testing.T) {
	f := newRefusalFixture(t)
	setRepoConfig(t, f.gitDir, versionConfig("repositoryformatversion = 2"))
	root := filepath.Dir(f.repo)

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
