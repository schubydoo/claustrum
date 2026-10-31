//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A baseRepo whose `.git` file names a git directory of mode 0600 answers not_a_repo
// on git.worktree_create, with and without worktreeRoot, and nothing on disk changes
// (683cc6a9, measured on Linux). Each git call in baseRepo carries GIT_DIR=<null
// device> (claustrum's own). Mutation: call requestGitDirTrust in
// gitWorktreeCreateLocked.
func TestWorktreeCreateUnsearchableGitFile(t *testing.T) {
	for _, withRoot := range []bool{false, true} {
		name := "no worktreeRoot"
		if withRoot {
			name = "worktreeRoot"
		}
		t.Run(name, func(t *testing.T) {
			log := unsearchableFixture(t)
			base := realTempDir(t)
			repo := filepath.Join(base, "T")
			if err := os.Mkdir(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "init", "-q")
			runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
			g := filepath.Join(base, "G")
			if err := os.Rename(filepath.Join(repo, ".git"), g); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(repo, ".git"), "gitdir: "+g+"\n", 0o644)
			params := map[string]any{"baseRepo": repo, "branchName": "b1",
				"worktreePath": filepath.Join(repo, ".claude", "worktrees", "w1")}
			if withRoot {
				root := filepath.Join(base, "root")
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatal(err)
				}
				params["worktreeRoot"] = root
				params["worktreePath"] = filepath.Join(root, "cp", "w1")
			}
			before := listTree(t, base)
			if err := os.WriteFile(log, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			noSearch(t, g)
			got := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create", params))
			wantFrame(t, got, `{"jsonrpc":"2.0","id":1,"result":{"success":false,`+
				`"error":"not a git repository","errorCode":"not_a_repo"}}`)
			inRepo := 0
			for _, call := range readEnvCalls(t, log) {
				if call.cwd != repo {
					continue
				}
				inRepo++
				if v, _ := envValue(call.env, "GIT_DIR"); v != os.DevNull {
					t.Errorf("git %q in %s has GIT_DIR=%q, want %s", call.argv, repo, v, os.DevNull)
				}
			}
			if inRepo == 0 {
				t.Errorf("no git call ran in %s, so the GIT_DIR test saw nothing", repo)
			}
			if err := os.Chmod(g, 0o755); err != nil {
				t.Fatal(err)
			}
			if after := listTree(t, base); !slices.Equal(after, before) {
				t.Errorf("the tree changed:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}
