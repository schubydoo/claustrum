//go:build !windows

package git

import (
	"encoding/json"
	"fmt"
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

// cannotLookCreate is the create result of 4c111e86 for a folder dir that the daemon
// cannot search for a `.git`. The path is dir with its symlinks resolved.
func cannotLookCreate(t *testing.T, dir string) string {
	t.Helper()
	h, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	text, err := json.Marshal("Claude couldn't look into `" + h + "` for a repository, so it made no worktree. " +
		"Check that your account on the remote machine may open that folder, or turn off Worktree.")
	if err != nil {
		t.Fatal(err)
	}
	return `{"success":false,"error":` + string(text) + `,"errorCode":"worktree_add_failed"}`
}

// chmodBack sets the mode of p and puts 0755 back at the end of the test.
func chmodBack(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
}

// A baseRepo that the daemon cannot search, with a `.git` folder in it, answers the
// couldn't-look refusal that names baseRepo, for mode 0600 and mode 0000. Nothing is
// made (4c111e86, measured on macOS). Linux is claustrum's own, not measured.
// Mutation: answer false in walkMayNotLook.
func TestCreateCannotLookIntoAFolder(t *testing.T) {
	requireGit(t)
	if os.Geteuid() == 0 {
		t.Skip("root passes the search test of every folder")
	}
	for _, mode := range []os.FileMode{0o600, 0} {
		t.Run(fmt.Sprintf("mode %04o", mode), func(t *testing.T) {
			tmp := realTempDir(t)
			requireTempOutsideCheckout(t, tmp)
			b := plainFolder(t, tmp)
			makeDir(t, filepath.Join(b, ".git"))
			want := cannotLookCreate(t, b)
			chmodBack(t, b, mode)
			r, _ := create(t, b)
			wantResult(t, "create", r, want)
			_ = os.Chmod(b, 0o755)
			if exists(filepath.Join(b, ".claude")) {
				t.Errorf("the create made %s", filepath.Join(b, ".claude"))
			}
		})
	}
}

// A `.git` folder that the daemon cannot search answers the unusable-git refusal: a
// repository whose `.git` has mode 0000 (4c111e86, measured on macOS), and an empty
// `.git` of mode 0600 (claustrum's own, not measured). Linux is claustrum's own, not
// measured. Nothing is made. Mutation: answer false in walkMayNotSearch.
func TestCreateRefusesAnUnsearchableGit(t *testing.T) {
	requireGit(t)
	if os.Geteuid() == 0 {
		t.Skip("root passes the search test of every folder")
	}
	for _, mode := range []os.FileMode{0, 0o600} {
		t.Run(fmt.Sprintf("mode %04o", mode), func(t *testing.T) {
			tmp := realTempDir(t)
			requireTempOutsideCheckout(t, tmp)
			b := filepath.Join(tmp, "B")
			if mode == 0 {
				initTrustMain(t, b)
			} else {
				b = plainFolder(t, tmp)
				makeDir(t, filepath.Join(b, ".git"))
			}
			chmodBack(t, filepath.Join(b, ".git"), mode)
			r, _ := create(t, b)
			wantResult(t, "create", r, unusableCreate(t, b))
			if exists(filepath.Join(b, ".claude")) {
				t.Errorf("the create made %s", filepath.Join(b, ".claude"))
			}
		})
	}
}

// A `.git` that is a link to itself or one of two links in a loop answers the
// unusable-git refusal, and a baseRepo that is a file answers not_a_repo (4c111e86,
// measured on macOS, the same as before). Linux is claustrum's own, not measured.
// Mutation: answer true for every error in walkMayNotLook.
func TestCreateLinkLoopAndFileBase(t *testing.T) {
	requireGit(t)
	t.Run("link to itself", func(t *testing.T) {
		tmp := realTempDir(t)
		requireTempOutsideCheckout(t, tmp)
		b := plainFolder(t, tmp)
		symlink(t, ".git", filepath.Join(b, ".git"))
		r, _ := create(t, b)
		wantResult(t, "create", r, unusableCreate(t, b))
	})
	t.Run("two links in a loop", func(t *testing.T) {
		tmp := realTempDir(t)
		requireTempOutsideCheckout(t, tmp)
		b := plainFolder(t, tmp)
		symlink(t, "b", filepath.Join(b, ".git"))
		symlink(t, ".git", filepath.Join(b, "b"))
		r, _ := create(t, b)
		wantResult(t, "create", r, unusableCreate(t, b))
	})
	t.Run("baseRepo is a file", func(t *testing.T) {
		tmp := realTempDir(t)
		requireTempOutsideCheckout(t, tmp)
		f := filepath.Join(tmp, "R")
		writeFile(t, f, "x\n", 0o644)
		r, _ := create(t, f)
		wantResult(t, "create", r, notRepoCreate)
	})
}

// An empty `.git` folder in a folder of another account answers not_a_repo (4c111e86,
// measured on macOS with that folder owned by root, with its `.git` owned by root or by the daemon account). The
// test stands for the other account with walkOwnUID. Linux is claustrum's own, not
// measured. 13047a51 is not measured for an empty `.git` on either system. Mutation: answer
// false in heldByOtherAccount.
func TestCreateKeepsNotARepoInAFolderOfAnotherAccount(t *testing.T) {
	requireGit(t)
	tmp := realTempDir(t)
	requireTempOutsideCheckout(t, tmp)
	b := plainFolder(t, tmp)
	makeDir(t, filepath.Join(b, ".git"))
	own := walkOwnUID
	t.Cleanup(func() { walkOwnUID = own })
	walkOwnUID = func() int { return os.Geteuid() + 1 }
	r, _ := create(t, b)
	wantResult(t, "create", r, notRepoCreate)
	if exists(filepath.Join(b, ".claude")) {
		t.Errorf("the create made %s", filepath.Join(b, ".claude"))
	}
}

// An empty `.git` folder in an own folder whose parent belongs to another account
// answers the unusable-git refusal (4c111e86, measured on macOS with a parent owned
// by root). The test needs a temp root of another account, such as /tmp. Mutation:
// test the owner of the parent in heldByOtherAccount.
func TestCreateRefusesAnEmptyGitBelowAFolderOfAnotherAccount(t *testing.T) {
	requireGit(t)
	top, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !heldByOtherAccount(top) {
		t.Skipf("%s belongs to this account", top)
	}
	b, err := os.MkdirTemp(top, "claustrum-owner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(b) })
	requireTempOutsideCheckout(t, b)
	makeDir(t, filepath.Join(b, ".git"))
	r, _ := create(t, b)
	wantResult(t, "create", r, unusableCreate(t, b))
}

// A `.git` link into a folder that the daemon cannot search answers the unusable-git
// refusal that names the link, as before the couldn't-look text (claustrum's own, not
// measured). Mutation: take the permission test from os.Stat in unusableDotGit.
func TestCreateGitLinkIntoAnUnsearchableFolder(t *testing.T) {
	requireGit(t)
	if os.Geteuid() == 0 {
		t.Skip("root passes the search test of every folder")
	}
	tmp := realTempDir(t)
	requireTempOutsideCheckout(t, tmp)
	b := plainFolder(t, tmp)
	locked := filepath.Join(tmp, "locked")
	makeDir(t, filepath.Join(locked, "g"))
	symlink(t, filepath.Join(locked, "g"), filepath.Join(b, ".git"))
	chmodBack(t, locked, 0)
	r, _ := create(t, b)
	wantResult(t, "create", r, unusableCreate(t, b))
}

// A `.git` folder of mode 0000 in a folder of another account answers not_a_repo: the
// owner test comes first (claustrum's own, not measured). The test stands for the other
// account with walkOwnUID. Mutation: test the owner only inside the search test, as in
// `!walkMayNotSearch(g) && (!gitDirUnusable(g) || heldByOtherAccount(d))`.
func TestCreateUnsearchableGitInAFolderOfAnotherAccount(t *testing.T) {
	requireGit(t)
	if os.Geteuid() == 0 {
		t.Skip("root passes the search test of every folder")
	}
	tmp := realTempDir(t)
	requireTempOutsideCheckout(t, tmp)
	b := plainFolder(t, tmp)
	makeDir(t, filepath.Join(b, ".git"))
	chmodBack(t, filepath.Join(b, ".git"), 0)
	own := walkOwnUID
	t.Cleanup(func() { walkOwnUID = own })
	walkOwnUID = func() int { return os.Geteuid() + 1 }
	r, _ := create(t, b)
	wantResult(t, "create", r, notRepoCreate)
}

// A good repository in a folder of another account, whose git refuses its owner, answers
// repository_of_another_account with the text of not_a_repo (13047a51, measured on Linux
// and macOS for a repository of root under a parent of root of mode 0555 or 0777, and
// under a parent of the daemon account). GIT_TEST_ASSUME_DIFFERENT_OWNER makes git
// refuse the owner, with no global config that trusts it, and walkOwnUID stands for the
// other account. With the folder of the
// daemon account the answer stays not_a_repo. Mutation: answer false in
// repoOfAnotherAccount.
func TestCreateAnswersARepositoryOfAnotherAccount(t *testing.T) {
	requireGit(t)
	tmp := realTempDir(t)
	requireTempOutsideCheckout(t, tmp)
	b := filepath.Join(tmp, "R")
	initTrustMain(t, b)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")
	r, _ := create(t, b)
	wantResult(t, "create in an own folder", r, notRepoCreate)
	own := walkOwnUID
	t.Cleanup(func() { walkOwnUID = own })
	walkOwnUID = func() int { return os.Geteuid() + 1 }
	r, _ = create(t, b)
	wantResult(t, "create", r, `{"success":false,"error":"not a git repository","errorCode":"repository_of_another_account"}`)
	if exists(filepath.Join(b, ".claude")) {
		t.Errorf("the create made %s", filepath.Join(b, ".claude"))
	}
}

// foreignFolders makes folderOwner answer another account for each folder of paths, and
// the real owner for every other folder.
func foreignFolders(t *testing.T, paths ...string) {
	t.Helper()
	real := folderOwner
	t.Cleanup(func() { folderOwner = real })
	folderOwner = func(d string) (int, bool) {
		for _, p := range paths {
			if filepath.Clean(d) == filepath.Clean(p) {
				return os.Geteuid() + 1, true
			}
		}
		return real(d)
	}
}

// ownerRefusedRepo makes a good repository at <tmp>/P/R, and makes git refuse its owner
// with no global config that trusts it (TestCreateAnswersARepositoryOfAnotherAccount).
func ownerRefusedRepo(t *testing.T) (parent, repo string) {
	t.Helper()
	requireGit(t)
	tmp := realTempDir(t)
	requireTempOutsideCheckout(t, tmp)
	parent = filepath.Join(tmp, "P")
	repo = filepath.Join(parent, "R")
	initTrustMain(t, repo)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")
	return parent, repo
}

// A refused repository of the daemon account below a parent of another account answers
// not_a_repo (claustrum's own, not measured). On 13047a51 git accepts such a repository
// (measured on Linux). Mutation: test the owner of the parent in repoOfAnotherAccount.
func TestCreateKeepsNotARepoForAnOwnRepoBelowAFolderOfAnotherAccount(t *testing.T) {
	parent, repo := ownerRefusedRepo(t)
	foreignFolders(t, parent)
	r, _ := create(t, repo)
	wantResult(t, "create", r, notRepoCreate)
}

// A repository folder of another account whose `.git` is the daemon account's answers
// repository_of_another_account (13047a51, measured on Linux). Mutation: test
// the owner of the `.git` in repoOfAnotherAccount.
func TestCreateAnswersAnotherAccountForAnOwnGitInAForeignFolder(t *testing.T) {
	_, repo := ownerRefusedRepo(t)
	foreignFolders(t, repo)
	r, _ := create(t, repo)
	wantResult(t, "create", r, `{"success":false,"error":"not a git repository","errorCode":"repository_of_another_account"}`)
}
