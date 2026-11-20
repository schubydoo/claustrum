package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// goneGitFileCreate makes a folder G whose `.git` file names a missing path, and
// returns the shadow fixture, G and the params of a create there.
func goneGitFileCreate(t *testing.T, off bool) (shadowFixture, string, map[string]any) {
	t.Helper()
	f := newShadowFixture(t)
	g := filepath.Join(resolveTestRoot(t, t.TempDir()), "G")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(g, ".git"), "gitdir: nowhere-dir\n", 0o644)
	params := map[string]any{"baseRepo": g, "worktreePath": filepath.Join(g, ".claude", "worktrees", "w1"), "branchName": "w1"}
	if off {
		params["shadowDirOff"] = true
	}
	return f, g, params
}

// TestCreateGoneGitFileStartTestStep runs the step of a create on a `.git` file
// to a missing path (gitWorktreeCreateLocked). 4c111e86 runs the
// start test and the call about the repository there, and after the reply the
// folder of the daemon holds its lock and `.probe` (measured on Linux and Windows). The stub
// gives the answers of an older git: the first run of the call exits 128 with "not
// a git repository", the second run exits 0, and the create answers the unusable
// refusal with no listing. A call that fails with exit 1 and a failed start test
// answer their text. With "shadowDirOff":true no test runs, and the stubbed listing
// gives the refusal. Those three rows are claustrum's own, not measured, except
// that 4c111e86 runs no test with the switch (measured on Linux). The text
// of git 2.56 is a Windows row (TestCreateGoneGitFileRunsTheStartTest). Mutation:
// drop the gate call, the askGit call or its "not a git repository" answer for the
// gone `.git` file in gitWorktreeCreateLocked.
func TestCreateGoneGitFileStartTestStep(t *testing.T) {
	older := "fatal: not a git repository: nowhere-dir\n"
	t.Run("shadow", func(t *testing.T) {
		f, g, params := goneGitFileCreate(t, false)
		answerProbe(t, 128, "", older)
		answerSecondProbe(t, 0, "")
		raw, calls := f.call(t, "git.worktree_create", params)
		if want := resultOf(unusableCreate(t, g)); raw != want {
			t.Errorf("git.worktree_create = %s\n  want %s", raw, want)
		}
		if len(testCalls(calls)) == 0 {
			t.Error("no start test ran")
		}
		if slices.ContainsFunc(calls, envCall.listing) {
			t.Error("a listing ran after the call about the repository")
		}
		names := entryNames(t, daemonFolder(t, f.run))
		for _, want := range []string{".probe", "lock"} {
			if !slices.Contains(names, want) {
				t.Errorf("the folder of the daemon holds %v, want %s in it", names, want)
			}
		}
	})
	t.Run("failed call", func(t *testing.T) {
		f, _, params := goneGitFileCreate(t, false)
		answerProbe(t, 1, "", "fatal: stub refuses the call\n")
		raw, calls := f.call(t, "git.worktree_create", params)
		if !strings.Contains(raw, "git could not check this repository on the remote machine") || !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
			t.Errorf("git.worktree_create = %s, want the text of the failed call", raw)
		}
		if slices.ContainsFunc(calls, envCall.listing) {
			t.Error("a listing ran after the failed call")
		}
	})
	t.Run("failed start test", func(t *testing.T) {
		f, _, params := goneGitFileCreate(t, false)
		failCallSeven(t, "fatal: stub refuses the test config\n")
		raw, _ := f.call(t, "git.worktree_create", params)
		if !strings.Contains(raw, "Claude could not test git on the remote machine") || !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
			t.Errorf("git.worktree_create = %s, want the text of the failed test", raw)
		}
	})
	t.Run("with the switch", func(t *testing.T) {
		f, g, params := goneGitFileCreate(t, true)
		stubListing(t, 128, "")
		stubStderr(t, older, false)
		raw, calls := f.call(t, "git.worktree_create", params)
		if want := resultOf(unusableCreate(t, g)); raw != want {
			t.Errorf("git.worktree_create = %s\n  want %s", raw, want)
		}
		if len(testCalls(calls)) != 0 {
			t.Error("a start test ran with the switch")
		}
	})
}
