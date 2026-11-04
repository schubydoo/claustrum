package git

import (
	"fmt"
	"path/filepath"
	"syscall"
	"testing"
)

// TestContentCheckLinkAtTheHash: a racer puts a link in place of a committed
// CLAUDE.md right before the `hash-object --stdin-paths` call. A loop gives the
// retry code with "too many levels of symbolic links" (b3081b51, measured on
// Linux for a circle of 3). The link to itself here stands in for that circle.
// A link through /proc/self/cwd out of the worktree gives it with "path escapes
// from parent" (measured on Linux). The rollback removes the worktree.
func TestContentCheckLinkAtTheHash(t *testing.T) {
	for _, row := range []struct {
		target, cause string
	}{
		{"CLAUDE.md", syscall.ELOOP.Error()},
		{"/proc/self/cwd/README", "path escapes from parent"},
	} {
		t.Run(row.target, func(t *testing.T) {
			f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", "README": "r\n"})
			p := filepath.Join(f.leaf, "CLAUDE.md")
			slowGit(t, "hash-object,--stdin-paths", "pre", 0, "", "")
			t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
			t.Setenv("CLAUSTRUM_GITSTUB_VALUE", "rm\x1f"+p+"\x1esymlink\x1f"+row.target+"\x1f"+p)
			raw, _ := f.create(t, nil)
			commit := gitRevParse(t, f.repo, "HEAD")
			hint := `Try again, and if this repeats check the permissions of the folder the worktree is made in: "CLAUDE.md" in the new worktree couldn't be read to check it against commit ` +
				commit[:12] + fmt.Sprintf(" (%s).", row.cause)
			if want := contentFrame(t, "worktree_verify_retry", hint); raw != want {
				t.Errorf("git.worktree_create =\n%s\nwant\n%s", raw, want)
			}
			f.wantGone(t)
		})
	}
}
