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
// unusable refusal (measured on Windows). The stub gives the answers of an older git:
// the first run of the call exits 128 with "not a git repository", the second run
// exits 0, and no listing follows. With the text of git 2.56 the listing runs
// (claustrum's own, not measured): its reply depends on the git of the host, so that
// row does not check it. With "shadowDirOff":true no test runs (claustrum's own, not
// measured), and the stubbed listing gives the refusal. Mutation: drop the gate call,
// the askGit call or its "not a git repository" answer for the gone `.git` file in
// gitWorktreeCreateLocked.
func TestCreateGoneGitFileRunsTheStartTest(t *testing.T) {
	older := "fatal: not a git repository: nowhere-dir\n"
	for _, row := range []struct {
		name, first string
		off         bool
	}{
		{"shadow", older, false},
		{"shadow, git 2.56 text", "fatal: gitfile does not point to a valid repository: <G>/.git\n", false},
		{"with the switch", "", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newShadowFixture(t)
			dir := resolveTestRoot(t, t.TempDir())
			g := filepath.Join(dir, "G")
			if err := os.MkdirAll(g, 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(g, ".git"), "gitdir: nowhere-dir\n", 0o644)
			params := map[string]any{"baseRepo": g, "worktreePath": filepath.Join(g, ".claude", "worktrees", "w1"), "branchName": "w1"}
			if row.off {
				params["shadowDirOff"] = true
				stubListing(t, 128, "")
				stubStderr(t, older, false)
			} else {
				answerProbe(t, 128, "", row.first)
				answerSecondProbe(t, 0, "")
			}
			raw, calls := f.call(t, "git.worktree_create", params)
			newer := row.first != older && !row.off
			if want := resultOf(unusableCreate(t, g)); !newer && raw != want {
				t.Errorf("git.worktree_create = %s\n  want %s", raw, want)
			}
			if got := len(testCalls(calls)) > 0; got != !row.off {
				t.Errorf("start test ran: %v, want %v", got, !row.off)
			}
			if row.off {
				return
			}
			if got := slices.ContainsFunc(calls, envCall.listing); got != newer {
				t.Errorf("a listing ran after the call about the repository: %v, want %v", got, newer)
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
