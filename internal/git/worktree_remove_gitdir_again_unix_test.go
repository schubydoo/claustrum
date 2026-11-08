//go:build !windows

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRemoveWithWorktreeRootAsksTheGitDirAgain: a remove with worktreeRoot in the
// state shadow, and a git that names another git directory for `rev-parse
// --absolute-git-dir`. c9125eb8 asks again, refuses with the "cannot determine
// the repository's git directory" text and the "disagree" text after 6 calls, and
// makes no `worktree list` call (measured on macOS). The worktree stays. A right
// second answer goes on, and the worktree goes (claustrum's own, not measured).
// Windows refuses a worktreeRoot before this check.
func TestRemoveWithWorktreeRootAsksTheGitDirAgain(t *testing.T) {
	for _, row := range []struct {
		name string
		// once is true when only the first call names the other folder.
		once bool
	}{
		{"every call", false},
		{"the first call only", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			root := dropGroupWrite(t, resolveTestRoot(t, t.TempDir()))
			requireTempOutsideCheckout(t, root)
			ext := filepath.Join(root, "proj", "wx")
			buildGit(t, f.repo, "worktree", "add", "-q", ext, "-b", "wx")
			other := stubOtherGitDir(t, row.once)
			raw, calls := f.call(t, "git.worktree_remove", map[string]any{
				"baseRepo": f.repo, "worktreePath": ext, "worktreeRoot": root})
			asked := gitDirAsks(calls)
			_, statErr := os.Lstat(ext)
			if row.once {
				if !strings.Contains(raw, `"success":true`) || asked != 3 || !os.IsNotExist(statErr) {
					t.Fatalf("git.worktree_remove = %s after %d calls, leaf: %v; want success after 3 calls, leaf gone", raw, asked, statErr)
				}
				return
			}
			want := gitDirUnknownPrefix + disagreeText(f.repo, other, f.gitDir)
			if got := removeErrorField(t, raw); got != want {
				t.Errorf("the error = %s\n    want %s", got, want)
			}
			if asked != 2 {
				t.Errorf("%d calls of rev-parse --absolute-git-dir, want 2", asked)
			}
			for _, c := range calls {
				if slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "list") {
					t.Errorf("a call %q ran, want none", c.argv)
				}
			}
			if statErr != nil {
				t.Errorf("the worktree: %v, want it kept", statErr)
			}
		})
	}
}
