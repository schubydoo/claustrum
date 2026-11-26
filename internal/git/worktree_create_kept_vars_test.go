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

// TestCreateUnderAnIgnoredVarOfNoFolder: in the state shadow with no request with
// "shadowDirOff":true, the Windows rule ignores the daemon variables, so
// keptVarDotGit names nothing. baseRepo holds an empty `.git` folder. A missing
// daemon GIT_DIR keeps not_a_repo, as any daemon GIT_DIR does there. A
// GIT_COMMON_DIR that is missing or a file gives the text of a daemon with no
// variable (both claustrum's own, not measured). Mutation: keptVarDotGit drops
// its test of daemonVarsKept.
func TestCreateUnderAnIgnoredVarOfNoFolder(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	file := func(t *testing.T) string {
		p := filepath.Join(t.TempDir(), "afile")
		writeFile(t, p, "x", 0o644)
		return p
	}
	for _, row := range []struct {
		name     string
		set      func(t *testing.T)
		notARepo bool
	}{
		{"missing GIT_DIR", func(t *testing.T) { t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "nogd", ".git")) }, true},
		{"missing GIT_COMMON_DIR", func(t *testing.T) { t.Setenv("GIT_COMMON_DIR", filepath.Join(t.TempDir(), "nogd", ".git")) }, false},
		{"GIT_COMMON_DIR of a file", func(t *testing.T) { t.Setenv("GIT_COMMON_DIR", file(t)) }, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			repo := filepath.Join(resolveTestRoot(t, t.TempDir()), "R")
			if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			row.set(t)
			leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
			raw, _ := f.call(t, "git.worktree_create", map[string]any{
				"baseRepo": repo, "branchName": "w1", "worktreePath": leaf})
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
				jsonString(t, "Git can't use `"+filepath.Join(repo, ".git")+"`, so Claude made no worktree. A Claude session can damage it, so check it first. Then repair it, or turn off Worktree if this isn't meant to be a repository.") +
				`,"errorCode":"worktree_add_failed"}}`
			if row.notARepo {
				want = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`
			}
			if raw != want {
				t.Errorf("reply = %s\nwant %s", raw, want)
			}
		})
	}
}

// TestNamesNoFolder: a missing path and a regular file name no folder. A folder
// and an empty value do not. Mutation: namesNoFolder drops its IsDir test.
func TestNamesNoFolder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	writeFile(t, file, "x", 0o644)
	for p, want := range map[string]bool{"": false, dir: false, file: true, filepath.Join(dir, "gone"): true} {
		if got := namesNoFolder(p); got != want {
			t.Errorf("namesNoFolder(%q) = %v, want %v", p, got, want)
		}
	}
}
