//go:build unix

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// With "shadowDirOff":true a locked entry that names the gone leaf gives no
// refusal of the daemon. The add runs, git answers its own text, and the record
// stays. 4c111e86 answers so (measured on macOS). Windows runs the staged add,
// and its text is not measured there, so the test runs on Linux and macOS.
func TestCreateOffLockedRegisteredLeafGetsGitsText(t *testing.T) {
	f := newRmFixture(t)
	dir := filepath.Join(t.TempDir(), sessiondirs.RecordFolderName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessiondirs.UseForTest(dir))
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w")
	runGit(t, f.repo, "worktree", "add", "-q", "-b", "old", leaf)
	runGit(t, f.repo, "worktree", "lock", leaf)
	if err := os.RemoveAll(leaf); err != nil {
		t.Fatal(err)
	}
	raw := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": f.repo, "branchName": "b", "worktreePath": leaf, "shadowDirOff": true}))
	text, code := createErrorField(t, raw)
	if !strings.HasPrefix(text, "git worktree add failed: ") || !strings.Contains(text, "missing but locked worktree") ||
		code != "worktree_add_failed" {
		t.Errorf("reply = %s\nwant git's own refusal of the locked leaf", raw)
	}
	if got, want := recordedPaths(t, dir), canonicalPath(filepath.Join(f.repo, ".git")); len(got) != 1 || got[0] != want {
		t.Errorf("after the refusal the records name %q, want %q", got, want)
	}
	mustExist(t, filepath.Join(f.repo, ".git", "worktrees", "w", "locked"))
}
