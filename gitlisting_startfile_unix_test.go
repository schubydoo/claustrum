//go:build !windows

package main

import (
	"path/filepath"
	"testing"
)

// Cells CeP0 and CeP3 (Linux VM). On Linux and macOS a regular file keeps its frames:
// the start error of the listing is no refusal there. 89cb6289 answers the same
// frames with nothing set and with an empty GIT_CONFIG_KEY_0. Mutation: set
// nonFolderStartRefuses to true on Linux and macOS.
func TestStartErrorInRegularFileKeepsFramesOnUnix(t *testing.T) {
	for _, ph := range []struct {
		name string
		env  []string
	}{
		{"CeP0 nothing set", nil},
		{"CeP3 empty key", brokenPairPhases[0].env},
	} {
		t.Run(ph.name, func(t *testing.T) {
			f := newNoRepoFixture(t)
			plantEnv(t, ph.env...)
			nFile := filepath.Join(f.n, "n.txt")
			writeFile(t, nFile, "n\n", 0o644)
			leaf := filepath.Join(nFile, ".claude", "worktrees", "w1")
			for _, row := range []struct {
				name, method string
				params       map[string]any
				want         string
			}{
				{"info file", "git.info", map[string]any{"path": nFile}, notRepoInfo},
				{"list_branches file", "git.list_branches", map[string]any{"path": nFile}, notRepoList},
				{"status path=file baseRepo=file", "git.status", map[string]any{"path": nFile, "baseRepo": nFile}, notRepoStatus},
				{"status path=N baseRepo=file", "git.status", map[string]any{"path": f.n, "baseRepo": nFile}, notRepoStatus},
				{"create baseRepo=file in-repo form", "git.worktree_create",
					map[string]any{"baseRepo": nFile, "branchName": "w1", "worktreePath": leaf}, notRepoCreate},
				{"create baseRepo=file worktreeRoot", "git.worktree_create",
					map[string]any{"baseRepo": nFile, "branchName": "w1", "worktreePath": f.external(), "worktreeRoot": f.r}, notRepoCreate},
			} {
				if got, want := f.frame(t, row.method, row.params), resultOf(row.want); got != want {
					t.Errorf("%s = %s\nwant %s", row.name, got, want)
				}
			}
		})
	}
}
