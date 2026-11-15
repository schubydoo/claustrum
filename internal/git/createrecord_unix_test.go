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

// A second create whose record was deleted after the first one writes the
// record again, also when its staged add then fails. Here the folder of the
// daemon in git-shadow takes no new entry (mode 0500). 4c111e86 answers the
// text of the private copy and keeps one record of `<repo>/.git` (measured on
// Linux). Without the test of a gone record (sessiondirs.RememberCreate) the
// second create writes none.
func TestCreateWritesAGoneRecordAgainBeforeAStageFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user makes an entry in a folder of mode 0500")
	}
	f := newSeededShadowFixture(t)
	records := filepath.Join(t.TempDir(), sessiondirs.RecordFolderName)
	if err := os.Mkdir(records, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessiondirs.UseForTest(records))
	first := filepath.Join(f.repo, ".claude", "worktrees", "w0")
	if raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w0", "worktreePath": first}); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("the first create = %s", raw)
	}
	entries, err := os.ReadDir(records)
	if err != nil || len(entries) != 1 {
		t.Fatalf("after the first create the record folder holds %v (%v), want one record", entries, err)
	}
	if err := os.Remove(filepath.Join(records, entries[0].Name())); err != nil {
		t.Fatal(err)
	}
	folder := daemonFolder(t, f.run)
	if err := os.Chmod(folder, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(folder, 0o700) })
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if got := createError(t, raw); !strings.Contains(got, privateCopyText) {
		t.Fatalf("the second create = %s, want the text of the private copy", raw)
	}
	if got, want := recordedPaths(t, records), canonicalPath(f.gitDir); len(got) != 1 || got[0] != want {
		t.Errorf("after the second create the records name %q, want %q", got, want)
	}
}
