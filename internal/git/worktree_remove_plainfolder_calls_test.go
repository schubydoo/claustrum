package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A remove in a folder where the trust check finds no repository: the listing
// passes, and `rev-parse --absolute-git-dir` fails. On Linux and macOS the two
// calls run once more (89cb6289, measured on Linux and macOS). On Windows they
// run one time, each with GIT_DIR=NUL, also with a daemon GIT_DIR (87375cc7,
// measured on Windows). The reply is the lock-check refusal, and the leaf stays.
// Mutation: drop the test of removeRechecksPlainFolder in methods_git.go.
func TestRemoveInPlainFolderChecksOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rechecks bool
		calls    []string
	}{
		{"repeat", true, []string{"list", "absolute-git-dir", "list", "absolute-git-dir"}},
		{"once", false, []string{"list", "absolute-git-dir"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newListingFixture(t)
			old := removeRechecksPlainFolder
			t.Cleanup(func() { removeRechecksPlainFolder = old })
			removeRechecksPlainFolder = tc.rechecks
			base := t.TempDir()
			requireTempOutsideCheckout(t, base)
			wp := filepath.Join(base, ".claude", "worktrees", "w1")
			if err := os.MkdirAll(wp, 0o755); err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(wp, "keep.txt")
			writeFile(t, keep, "k\n", 0o644)
			raw, calls := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": base, "worktreePath": wp})
			want := `{"success":false,"error":` + jsonString(t, lockCheckRefusal(wp)) + `}`
			if got := replyPart(t, raw); got != want {
				t.Errorf("remove = %s\nwant %s", got, want)
			}
			if got := relistCalls(calls); !slices.Equal(got, tc.calls) {
				t.Errorf("calls = %q\nwant %q", got, tc.calls)
			}
			for i, c := range calls {
				var dirs []string
				for _, kv := range c.env {
					if strings.HasPrefix(kv, "GIT_DIR=") {
						dirs = append(dirs, kv)
					}
				}
				if !slices.Equal(dirs, []string{"GIT_DIR=" + os.DevNull}) {
					t.Errorf("call %d %q has %q, want GIT_DIR=%s alone", i, c.argv, dirs, os.DevNull)
				}
			}
			if !exists(keep) {
				t.Error("keep.txt went")
			}
		})
	}
}
