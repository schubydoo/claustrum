package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreateUnderAKeptVarOfNoFolder: with the Windows rule (copyPinsWalkGitDir)
// and "shadowDirOff":true, a daemon GIT_DIR or GIT_COMMON_DIR that names no
// folder makes git find no repository at a good baseRepo. 87375cc7 answers
// worktree_add_failed with unusableGitText for `<baseRepo>/.git` there (measured
// on Windows). With the rule of Linux and macOS the create keeps not_a_repo.
// Mutation: in unusableDotGit return "" in place of keptVarDotGit(dir).
func TestCreateUnderAKeptVarOfNoFolder(t *testing.T) {
	file := func(t *testing.T) string {
		p := filepath.Join(t.TempDir(), "afile")
		writeFile(t, p, "x", 0o644)
		return p
	}
	for _, row := range []struct {
		name string
		set  func(t *testing.T)
	}{
		{"missing GIT_DIR", func(t *testing.T) { t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "nogd", ".git")) }},
		{"missing GIT_COMMON_DIR", func(t *testing.T) { t.Setenv("GIT_COMMON_DIR", filepath.Join(t.TempDir(), "nogd", ".git")) }},
		{"GIT_COMMON_DIR of a file", func(t *testing.T) { t.Setenv("GIT_COMMON_DIR", file(t)) }},
	} {
		for _, windows := range []bool{true, false} {
			name := row.name + " Linux rule"
			if windows {
				name = row.name + " Windows rule"
			}
			t.Run(name, func(t *testing.T) {
				setCopyPinsWalkGitDir(t, windows)
				f := newShadowFixture(t)
				repo, gitDir := configRepo(t)
				row.set(t)
				leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
				raw, _ := f.call(t, "git.worktree_create", map[string]any{
					"baseRepo": repo, "branchName": "w1", "worktreePath": leaf, "shadowDirOff": true})
				want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`
				if windows {
					want = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
						jsonString(t, "Git can't use `"+gitDir+"`, so Claude made no worktree. A Claude session can damage it, so check it first. Then repair it, or turn off Worktree if this isn't meant to be a repository.") +
						`,"errorCode":"worktree_add_failed"}}`
				}
				if raw != want {
					t.Errorf("reply = %s\nwant %s", raw, want)
				}
				if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
					t.Errorf("the leaf: %v, want none", err)
				}
			})
		}
	}
}
