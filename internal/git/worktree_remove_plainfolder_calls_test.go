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
// run one time, each with GIT_DIR=NUL, also with an absolute daemon GIT_DIR that
// names the .git folder of a plain repository (87375cc7, measured on Windows; the
// remove page has the other kinds). That row ran in the state shadow. The
// reply is the lock-check refusal, and the leaf stays. The row "once, daemon
// GIT_DIR" sets GIT_DIR to the git directory of a real repository. With
// copyPinsWalkGitDir on, the walk gives the plain folder gitDirNoRepo.
// Mutations: drop the test of removeRechecksPlainFolder in methods_git.go, and
// set copyPinsWalkGitDir off in the row of the daemon GIT_DIR.
func TestRemoveInPlainFolderChecksOnce(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rechecks     bool
		daemonGitDir bool
		calls        []string
	}{
		{"repeat", true, false, []string{"list", "absolute-git-dir", "list", "absolute-git-dir"}},
		{"once", false, false, []string{"list", "absolute-git-dir"}},
		{"once, daemon GIT_DIR", false, true, []string{"list", "absolute-git-dir"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var call func(*testing.T, string, map[string]any) (string, []envCall)
			if tc.daemonGitDir {
				call = newShadowFixture(t).call
				setCopyPinsWalkGitDir(t, true)
				_, gitDir := configRepo(t)
				t.Setenv("GIT_DIR", gitDir)
			} else {
				call = newListingFixture(t).call
			}
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
			raw, calls := call(t, "git.worktree_remove", map[string]any{"baseRepo": base, "worktreePath": wp})
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
