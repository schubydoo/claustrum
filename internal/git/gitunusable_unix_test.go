//go:build !windows

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// A dangling `.git` link with no repository above answers the unusable-git refusal.
// Below a good repository the walk goes on, and the create makes the worktree there.
// With worktreeRoot the refusal is the same, and nothing is made (4c111e86, measured
// on Linux and macOS). Mutations: answer not_a_repo in notARepoResult, or pass over a
// dangling link in unusableDotGit without naming it.
func TestCreateRefusesADanglingGitLink(t *testing.T) {
	requireGit(t)
	t.Run("no repository above", func(t *testing.T) {
		tmp := realTempDir(t)
		requireTempOutsideCheckout(t, tmp)
		b := plainFolder(t, tmp)
		symlink(t, "nowhere", filepath.Join(b, ".git"))
		r, _ := create(t, b)
		wantResult(t, "create", r, unusableCreate(t, b))
		root := filepath.Join(tmp, "root")
		makeDir(t, root)
		r = trustCall(t, "git.worktree_create", map[string]any{"baseRepo": b, "branchName": "w2",
			"worktreePath": filepath.Join(root, "proj", "w2"), "worktreeRoot": root})
		wantResult(t, "create with worktreeRoot", r, unusableCreate(t, b))
		if exists(filepath.Join(b, ".claude")) || exists(filepath.Join(root, "proj")) {
			t.Errorf("a refused create made a folder")
		}
	})
	t.Run("a good repository above", func(t *testing.T) {
		tmp := realTempDir(t)
		v := filepath.Join(tmp, "V")
		initTrustMain(t, v)
		sub := filepath.Join(v, "sub")
		writeFile(t, filepath.Join(sub, "README"), "x\n", 0o644)
		symlink(t, "nowhere", filepath.Join(sub, ".git"))
		r, _ := create(t, sub)
		wantResultPrefix(t, "create", r, `{"success":true`)
	})
}

// Where the walk meets a dangling `.git` link and then an empty `.git` folder above
// it, the refusal names the link, the first one (claustrum's own, not measured).
// Mutation: name the folder in unusableDotGit even when a name is set.
func TestCreateNamesTheFirstUnusableGit(t *testing.T) {
	requireGit(t)
	tmp := realTempDir(t)
	requireTempOutsideCheckout(t, tmp)
	b := plainFolder(t, tmp)
	makeDir(t, filepath.Join(b, ".git"))
	sub := filepath.Join(b, "sub")
	makeDir(t, sub)
	symlink(t, "nowhere", filepath.Join(sub, ".git"))
	r, _ := create(t, sub)
	wantResult(t, "create", r, unusableCreate(t, sub))
}

// A `.git` folder that the daemon cannot search keeps not_a_repo, as 683cc6a9
// answers (claustrum's own on 4c111e86, not measured). Nothing is made. Mutation:
// let the gitDirUnsearchable test in gitDirUnusable answer unusable.
func TestCreateKeepsNotARepoForAnUnsearchableGit(t *testing.T) {
	requireGit(t)
	if os.Geteuid() == 0 {
		t.Skip("root passes the search test of every folder")
	}
	tmp := realTempDir(t)
	requireTempOutsideCheckout(t, tmp)
	b := plainFolder(t, tmp)
	g := filepath.Join(b, ".git")
	makeDir(t, g)
	if err := os.Chmod(g, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(g, 0o755) })
	r, _ := create(t, b)
	wantResult(t, "create", r, notRepoCreate)
	if exists(filepath.Join(b, ".claude")) {
		t.Errorf("the create made %s", filepath.Join(b, ".claude"))
	}
}
