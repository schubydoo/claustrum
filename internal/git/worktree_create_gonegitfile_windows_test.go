//go:build windows

package git

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestCreateGoneGitFileRunsTheStartTest pins git.worktree_create as the first git
// request of a daemon on Windows, on a folder whose `.git` file names a missing path.
// 4c111e86 runs the start test and the call about the repository there. After the
// reply the folder of the daemon holds its lock and `.probe`, and the reply is the
// unusable refusal (measured on Windows). With "shadowDirOff":true no test runs
// (claustrum's own, not measured).
// Mutation: drop the gate call, the askGit call or its "not a git repository"
// answer for the gone `.git` file in gitWorktreeCreateLocked.
func TestCreateGoneGitFileRunsTheStartTest(t *testing.T) {
	for _, off := range []bool{false, true} {
		t.Run(map[bool]string{false: "shadow", true: "with the switch"}[off], func(t *testing.T) {
			f := newShadowFixture(t)
			dir := resolveTestRoot(t, t.TempDir())
			g := filepath.Join(dir, "G")
			if err := os.MkdirAll(g, 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(g, ".git"), "gitdir: nowhere-dir\n", 0o644)
			params := map[string]any{"baseRepo": g, "worktreePath": filepath.Join(g, ".claude", "worktrees", "w1"), "branchName": "w1"}
			if off {
				params["shadowDirOff"] = true
			}
			raw, calls := f.call(t, "git.worktree_create", params)
			if want := resultOf(unusableCreate(t, g)); raw != want {
				t.Errorf("git.worktree_create = %s\n  want %s", raw, want)
			}
			if got := len(testCalls(calls)) > 0; got != !off {
				t.Errorf("start test ran: %v, want %v", got, !off)
			}
			if off {
				return
			}
			// git answers "not a git repository" there, so no listing follows.
			for _, c := range calls {
				if c.listing() {
					t.Errorf("a listing ran after the call about the repository: %q", c.argv)
				}
			}
			names := entryNames(t, daemonFolder(t, f.run))
			for _, want := range []string{".probe", "lock"} {
				if !slices.Contains(names, want) {
					t.Errorf("the folder of the daemon holds %v, want %s in it", names, want)
				}
			}
		})
	}
}
