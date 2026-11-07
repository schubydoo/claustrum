package git

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCreateAsksTheGitDirAgain: a git that names another git directory for
// `rev-parse --absolute-git-dir` in the state shadow. c9125eb8 asks again
// (measured on Linux and Windows). With a right second answer the create
// succeeds. With a wrong one it fails after 2 calls with the "disagree" text,
// runs no `worktree add` and leaves no folder of the new worktree.
func TestCreateAsksTheGitDirAgain(t *testing.T) {
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
			other := filepath.Join(t.TempDir(), "other", ".git")
			if err := os.MkdirAll(other, 0o755); err != nil {
				t.Fatal(err)
			}
			// The stub reads \n, \r and \t in its output as escapes, so the
			// answer has forward slashes, as git prints it on Windows.
			stubAnswer(t, "rev-parse,--absolute-git-dir", 0, filepath.ToSlash(other)+`\n`)
			if row.once {
				t.Setenv("CLAUSTRUM_GITSTUB_NTHFILE", filepath.Join(t.TempDir(), "nth"))
				t.Setenv("CLAUSTRUM_GITSTUB_NTH", "1")
			}
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			if row.once {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("git.worktree_create = %s, want success", raw)
				}
				return
			}
			want := "git worktree add failed: cannot locate the repository's git directory: " +
				disagreeText(f.repo, filepath.ToSlash(other), f.gitDir)
			if got, code := createErrorField(t, raw); got != want || code != "worktree_add_failed" {
				t.Errorf("the error = %s (%s)\n    want %s (worktree_add_failed)", got, code, want)
			}
			asked := 0
			for _, c := range calls {
				if slices.Contains(c.argv, "--absolute-git-dir") {
					asked++
				}
				if slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add") {
					t.Errorf("a worktree add call ran: %q", c.argv)
				}
			}
			if asked != 2 {
				t.Errorf("%d calls of rev-parse --absolute-git-dir, want 2", asked)
			}
			if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
				t.Errorf("the leaf: %v, want it gone", err)
			}
		})
	}
}

// TestCreateFromALinkedWorktreeAsksOnce: for a linked worktree as baseRepo git
// names the entry of that worktree, which is no other git directory. The create
// asks one time and succeeds (claustrum's own, not measured).
func TestCreateFromALinkedWorktreeAsksOnce(t *testing.T) {
	f := newSeededShadowFixture(t)
	lw := filepath.Join(resolveTestRoot(t, t.TempDir()), "lw")
	buildGit(t, f.repo, "worktree", "add", "-q", lw, "-b", "lw")
	leaf := filepath.Join(lw, ".claude", "worktrees", "w2")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": lw, "branchName": "w2", "worktreePath": leaf})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	asked := 0
	for _, c := range calls {
		if slices.Contains(c.argv, "--absolute-git-dir") {
			asked++
		}
	}
	if asked != 1 {
		t.Errorf("%d calls of rev-parse --absolute-git-dir, want 1", asked)
	}
}

// TestCreateFromALinkedWorktreeNamesItsEntry: for a linked worktree as baseRepo
// and a git that names another git directory on every call, the refusal names
// the entry of that worktree as the folder that Claude finds (claustrum's own,
// not measured).
func TestCreateFromALinkedWorktreeNamesItsEntry(t *testing.T) {
	f := newSeededShadowFixture(t)
	lw := filepath.Join(resolveTestRoot(t, t.TempDir()), "lw")
	buildGit(t, f.repo, "worktree", "add", "-q", lw, "-b", "lw")
	other := filepath.Join(t.TempDir(), "other", ".git")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	stubAnswer(t, "rev-parse,--absolute-git-dir", 0, filepath.ToSlash(other)+`\n`)
	leaf := filepath.Join(lw, ".claude", "worktrees", "w2")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": lw, "branchName": "w2", "worktreePath": leaf})
	want := fmt.Sprintf("git worktree add failed: cannot locate the repository's git directory: "+
		"git and Claude disagree about which repository holds this folder: from %q git finds %q, Claude finds %q; try again",
		lw, filepath.ToSlash(other), filepath.Join(f.gitDir, "worktrees", "lw"))
	if got, code := createErrorField(t, raw); got != want || code != "worktree_add_failed" {
		t.Errorf("the error = %s (%s)\n    want %s (worktree_add_failed)", got, code, want)
	}
}
