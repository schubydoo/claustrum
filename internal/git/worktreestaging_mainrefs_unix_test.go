//go:build linux || darwin

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateWithAReftableLinkOfTheGitDirectoryOnUnix runs git.worktree_create
// with the default of Linux and macOS on a repository whose `.git/reftable` is
// a link to a folder outside the git directory. Those systems do not take the
// step of Windows (mainRefsOpenTest), so the create succeeds (not measured).
func TestCreateWithAReftableLinkOfTheGitDirectoryOnUnix(t *testing.T) {
	if mainRefsOpenTest {
		t.Fatal("mainRefsOpenTest is true on Linux and macOS, want false")
	}
	f := newSeededShadowFixture(t)
	out := filepath.Join(t.TempDir(), "rt-out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(out, "tables.list"), "")
	linkAt(t, filepath.Join(f.gitDir, "reftable"), out)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if !strings.Contains(raw, `"success":true`) {
		t.Errorf("git.worktree_create = %s, want success", raw)
	}
}
