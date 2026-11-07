package git

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// removeDisagreeText is the reply text of a remove of wt from repo whose git
// answers other twice, where the daemon finds want. The form is that of the
// rows of c9125eb8 on Linux and Windows.
func removeDisagreeText(wt, repo, other, want string) string {
	return fmt.Sprintf("failed to remove worktree: could not check whether %s is locked: "+
		"git and Claude disagree about which repository holds this folder: from %q git finds %q, Claude finds %q; try again",
		wt, repo, other, want)
}

// stubOtherGitDir makes `rev-parse --absolute-git-dir` print the path of a
// folder other/.git that exists. With once set, only the first call does. It
// returns the path as the stub prints it.
func stubOtherGitDir(t *testing.T, once bool) string {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other", ".git")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	// The stub reads \n, \r and \t in its output as escapes, so the
	// answer has forward slashes, as git prints it on Windows.
	stubAnswer(t, "rev-parse,--absolute-git-dir", 0, filepath.ToSlash(other)+`\n`)
	if once {
		t.Setenv("CLAUSTRUM_GITSTUB_NTHFILE", filepath.Join(t.TempDir(), "nth"))
		t.Setenv("CLAUSTRUM_GITSTUB_NTH", "1")
	}
	return filepath.ToSlash(other)
}

// gitDirAsks counts the `rev-parse --absolute-git-dir` calls of calls.
func gitDirAsks(calls []envCall) int {
	asked := 0
	for _, c := range calls {
		if slices.Contains(c.argv, "--absolute-git-dir") {
			asked++
		}
	}
	return asked
}

// TestRemoveAsksTheGitDirAgain: a git that names another git directory for
// `rev-parse --absolute-git-dir` in the state shadow. c9125eb8 asks again
// (measured on Linux and Windows). With a right second answer the remove
// succeeds after 2 calls. With a wrong one it refuses after 4 calls with the
// "disagree" text, and the worktree stays.
func TestRemoveAsksTheGitDirAgain(t *testing.T) {
	for _, row := range []struct {
		name string
		// once is true when only the first call names the other folder.
		once bool
	}{
		{"the first call only", true},
		{"every call", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			other := stubOtherGitDir(t, row.once)
			raw, calls := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.wt})
			asked := gitDirAsks(calls)
			_, statErr := os.Lstat(f.wt)
			if row.once {
				if !strings.Contains(raw, `"success":true`) || asked != 2 || !os.IsNotExist(statErr) {
					t.Fatalf("git.worktree_remove = %s after %d calls, leaf: %v; want success after 2 calls, leaf gone", raw, asked, statErr)
				}
				return
			}
			if got, want := removeErrorField(t, raw), removeDisagreeText(f.wt, f.repo, other, f.gitDir); got != want {
				t.Errorf("the error = %s\n    want %s", got, want)
			}
			if asked != 4 {
				t.Errorf("%d calls of rev-parse --absolute-git-dir, want 4", asked)
			}
			if statErr != nil {
				t.Errorf("the worktree: %v, want it kept", statErr)
			}
		})
	}
}

// TestRemoveAsksTheGitDirOnce: outside the limits of the second ask, a remove
// asks one time and uses the answer as before (claustrum's own, not measured).
// With "shadowDirOff":true the request has no copy, and the worktree goes. With
// a GIT_DIR in the daemon's environment no second call runs.
func TestRemoveAsksTheGitDirOnce(t *testing.T) {
	t.Run("shadowDirOff", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		stubOtherGitDir(t, true)
		raw, calls := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "shadowDirOff": true})
		_, statErr := os.Lstat(f.wt)
		if asked := gitDirAsks(calls); !strings.Contains(raw, `"success":true`) || asked != 1 || !os.IsNotExist(statErr) {
			t.Fatalf("git.worktree_remove = %s after %d calls, leaf: %v; want success after 1 call, leaf gone", raw, asked, statErr)
		}
	})
	t.Run("daemon GIT_DIR", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		t.Setenv("GIT_DIR", f.gitDir)
		stubOtherGitDir(t, false)
		raw, calls := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.wt})
		if asked := gitDirAsks(calls); asked != 1 || strings.Contains(raw, "disagree") {
			t.Fatalf("git.worktree_remove = %s after %d calls, want 1 call and no disagree text", raw, asked)
		}
	})
}

// TestGitDirAgainNeedsAPin: a trust verdict with no pinned git directory gets no
// second call (claustrum's own, not measured). A remove does not reach this
// state: a baseRepo that exists gets a pin, and a missing one skips the ask.
// So the row calls gitDirAgain itself.
func TestGitDirAgainNeedsAPin(t *testing.T) {
	f := newSeededShadowFixture(t)
	stubOtherGitDir(t, false)
	if err := os.WriteFile(f.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	answered := filepath.Join(t.TempDir(), "elsewhere", ".git")
	got, disagree, err := gitDirAgain(gitUse{repoDir: f.run}, f.repo, gitDirTrust{}, answered)
	if asked := gitDirAsks(readEnvCalls(t, f.log)); got != answered || disagree != "" || err != nil || asked != 0 {
		t.Errorf("gitDirAgain = %q, %q, %v after %d calls, want %q with no call", got, disagree, err, asked, answered)
	}
}

// TestRemoveFromALinkedWorktreeNamesItsEntry: for a linked worktree as baseRepo
// the "disagree" text names the entry of that worktree as the folder that the
// daemon finds (claustrum's own, not measured).
func TestRemoveFromALinkedWorktreeNamesItsEntry(t *testing.T) {
	f := newSeededShadowFixture(t)
	lw := filepath.Join(resolveTestRoot(t, t.TempDir()), "lw")
	buildGit(t, f.repo, "worktree", "add", "-q", lw, "-b", "lw")
	wt := filepath.Join(lw, ".claude", "worktrees", "w2")
	buildGit(t, lw, "worktree", "add", "-q", wt, "-b", "w2")
	other := stubOtherGitDir(t, false)
	raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": lw, "worktreePath": wt})
	want := removeDisagreeText(wt, lw, other, filepath.Join(f.gitDir, "worktrees", "lw"))
	if got := removeErrorField(t, raw); got != want {
		t.Errorf("the error = %s\n    want %s", got, want)
	}
}
