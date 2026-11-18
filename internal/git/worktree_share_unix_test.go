//go:build unix

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShareGitDirUnderMissingDaemonGitDir: a create on a share whose "no
// repository" comes from a daemon GIT_DIR that names a missing path, and not
// from askGit, keeps not_a_repo (gitDirTrust.goneEnvGitDir). The share step
// needs the answer of askGit (claustrum's own, not measured).
func TestShareGitDirUnderMissingDaemonGitDir(t *testing.T) {
	f := newShadowFixture(t)
	base := resolveTestRoot(t, t.TempDir())
	requireTempOutsideCheckout(t, base)
	dir := filepath.Join(base, "F")
	makeDir(t, filepath.Join(dir, ".git"))
	t.Setenv("GIT_DIR", filepath.Join(base, "nonexist"))
	old := repoOnShare
	repoOnShare = func(repo string) bool { return repo == dir }
	t.Cleanup(func() { repoOnShare = old })
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": dir, "branchName": "w1",
		"worktreePath": filepath.Join(dir, ".claude", "worktrees", "w1")})
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`; raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
}

// shareTestLink links link to target with a symbolic link (the link row of
// TestShareGitDirThatGitDoesNotUse).
func shareTestLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
