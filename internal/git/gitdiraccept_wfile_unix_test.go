//go:build unix

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestWindowsCreateWithAFileAtWorktrees: a regular file or a link at
// `.git/worktrees`, with the steps of Windows switched on. For a file in a new
// branch 87375cc7 and 4c111e86 answer `cannot add a worktree: ` and the text,
// after the show-toplevel pair. In attach mode 87375cc7 answers the same with
// one more listing (measured on Windows). A junction there gets the same in a new branch (87375cc7, measured
// on Windows, a link stands for it here). With "shadowDirOff":true the text of
// the off refusal gets the same start, with no pair (measured on Windows for
// both kinds). No `worktree add` runs.
func TestWindowsCreateWithAFileAtWorktrees(t *testing.T) {
	// The Windows rows have no second attach listing, so the macOS one stays off here.
	oldList := attachAddListsTwice
	attachAddListsTwice = false
	t.Cleanup(func() { attachAddListsTwice = oldList })
	const prepare = "cannot prepare the repository's worktrees directory: %q is not a directory"
	const list = "cannot list the repository's worktree entries: \"worktrees\" is a link or a file where Claude needs a real directory; put the directory itself in its place"
	for _, row := range []struct {
		name     string
		link     bool
		extra    map[string]any
		text     string
		pairs    int
		listings int
	}{
		{"a new branch", false, nil, prepare, 1, 4},
		{"attach", false, map[string]any{"existingBranch": "eb"}, prepare, 1, 6},
		{"shadowDirOff", false, map[string]any{"shadowDirOff": true}, list, 0, 4},
		{"a link, a new branch", true, nil, prepare, 1, 4},
		{"a link, attach", true, map[string]any{"existingBranch": "eb"}, prepare, 1, 6},
		{"a link, shadowDirOff", true, map[string]any{"shadowDirOff": true}, list, 0, 4},
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
			if row.link {
				linkAt(t, worktrees, t.TempDir())
			} else {
				mustWrite(t, worktrees, "x\n")
			}
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
			if fi, err := os.Lstat(worktrees); err != nil || fi.IsDir() {
				t.Errorf("the entry at worktrees: %v, want it kept", err)
			}
		})
	}
}
