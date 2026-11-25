//go:build unix

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestWindowsCreateWithAFileAtWorktrees: a regular file at `.git/worktrees`,
// with the steps of Windows switched on. 87375cc7 and 4c111e86 answer
// `cannot add a worktree: ` and the text, after the show-toplevel pair, and an
// attach add makes one more listing (measured on Windows). With
// "shadowDirOff":true the text of the off refusal gets the same start, with no
// pair (measured on Windows). No `worktree add` runs.
func TestWindowsCreateWithAFileAtWorktrees(t *testing.T) {
	for _, row := range []struct {
		name     string
		extra    map[string]any
		text     string
		pairs    int
		listings int
	}{
		{"a new branch", nil, "cannot prepare the repository's worktrees directory: %q is not a directory", 1, 4},
		{"attach", map[string]any{"existingBranch": "eb"}, "cannot prepare the repository's worktrees directory: %q is not a directory", 1, 6},
		{"shadowDirOff", map[string]any{"shadowDirOff": true}, "cannot list the repository's worktree entries: \"worktrees\" is a link or a file where Claude needs a real directory; put the directory itself in its place", 0, 4},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			runGit(t, f.repo, "branch", "eb")
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			worktrees := filepath.Join(f.gitDir, worktreesSubdir)
			if err := os.Rename(worktrees, worktrees+"-kept"); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			mustWrite(t, worktrees, "x\n")
			setStagedAddWindowsSteps(t, true)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			params := map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}
			for k, v := range row.extra {
				params[k] = v
			}
			raw, calls := f.call(t, "git.worktree_create", params)
			want := strings.Replace("cannot add a worktree: "+row.text, "%q", quoteOperand(worktrees), 1)
			if text, code := createErrorField(t, raw); text != want || code != "worktree_add_failed" {
				t.Errorf("git.worktree_create = %s\nwant the error %s", raw, want)
			}
			pairs, listings := 0, 0
			for _, c := range calls {
				named := slices.ContainsFunc(c.argv, func(a string) bool { return strings.HasPrefix(a, "--git-dir=") })
				switch {
				case slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add"):
					t.Errorf("a worktree add call ran: %q", c.argv)
				case named && slices.Contains(c.argv, "--show-toplevel"):
					pairs++
				case !named && c.listing():
					listings++
				}
			}
			if pairs != row.pairs || listings != row.listings {
				t.Errorf("the create made %d pairs and %d listings, want %d and %d", pairs, listings, row.pairs, row.listings)
			}
			if got := mustRead(t, worktrees); got != "x\n" {
				t.Errorf("the file at worktrees = %q", got)
			}
		})
	}
}
