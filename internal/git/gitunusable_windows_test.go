//go:build windows

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestRepoOnShare pins the paths that count as a network share (shareGitDir). The
// share of the row is `\\127.0.0.1\<share>\repo` (4c111e86, measured on Windows).
// The other forms are claustrum's own, not measured.
func TestRepoOnShare(t *testing.T) {
	for _, row := range []struct {
		path string
		want bool
	}{
		{`\\127.0.0.1\s\repo`, true},
		{`//127.0.0.1/s/repo`, true},
		{`\\host\share`, true},
		{`C:\x\repo`, false},
		{`\\?\C:\x\repo`, false},
		{`\\?\UNC\host\share\repo`, false},
		{`\\.\pipe\x`, false},
	} {
		if got := repoOnShare(row.path); got != row.want {
			t.Errorf("repoOnShare(%q) = %v, want %v", row.path, got, row.want)
		}
	}
}

// shareTestLink links link to target with a junction (the link row of
// TestShareGitDirThatGitDoesNotUse).
func shareTestLink(t *testing.T, link, target string) {
	t.Helper()
	makeJunction(t, link, target)
}

// A good repository in a folder of another owner, whose git refuses that owner, answers
// the unusable-git refusal that names its `.git` (87375cc7 and 13047a51, measured on
// Windows for a repository folder owned by SYSTEM). GIT_TEST_ASSUME_DIFFERENT_OWNER makes
// git refuse the owner, with no global config that trusts it. With the owner of the
// daemon account the answer stays not_a_repo. Mutation: answer false in the stand-in of
// repoOfAnotherAccount.
func TestCreateRefusesARepositoryOfAnotherOwner(t *testing.T) {
	requireGit(t)
	tmp := realTempDir(t)
	requireTempOutsideCheckout(t, tmp)
	b := filepath.Join(tmp, "R")
	initTrustMain(t, b)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")
	r, _ := create(t, b)
	wantResult(t, "create with an own owner", r, notRepoCreate)
	own := repoOfAnotherAccount
	t.Cleanup(func() { repoOfAnotherAccount = own })
	repoOfAnotherAccount = func(string) bool { return true }
	r, _ = create(t, b)
	wantResult(t, "create", r, unusableCreate(t, b))
	if exists(filepath.Join(b, ".claude")) {
		t.Errorf("the create made %s", filepath.Join(b, ".claude"))
	}
}

// A folder that the test made belongs to the user of the daemon's token (from the
// code). Mutation: answer true in repoOfAnotherAccount.
func TestRepoOfAnotherAccountOwnFolder(t *testing.T) {
	if repoOfAnotherAccount(t.TempDir()) {
		t.Error("a folder of this account counts as another account's")
	}
}

// A folder owned by SYSTEM counts as another account's (87375cc7 and 13047a51, owner
// SYSTEM, measured on Windows). One owned by the Administrators group does not while the
// elevated token is a member (claustrum's own, not measured). icacls sets each owner,
// which needs an elevated token. Mutations: answer false in repoOfAnotherAccount, or drop
// its Administrators test.
func TestRepoOfAnotherAccountOwners(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("setting another owner needs an elevated token, and this one is not")
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		owner string
		want  bool
	}{
		{"*S-1-5-18", true},
		{"*S-1-5-32-544", false},
	} {
		d := t.TempDir()
		setOwner(t, d, row.owner)
		t.Cleanup(func() { setOwner(t, d, "*"+u.User.Sid.String()) })
		if got := repoOfAnotherAccount(d); got != row.want {
			t.Errorf("owner %s: repoOfAnotherAccount = %v, want %v", row.owner, got, row.want)
		}
	}
}

// setOwner makes owner, a SID form of icacls, the owner of d.
func setOwner(t *testing.T, d, owner string) {
	t.Helper()
	if out, err := exec.Command("icacls", d, "/setowner", owner).CombinedOutput(); err != nil {
		t.Fatalf("icacls %s /setowner %s: %v\n%s", d, owner, err, out)
	}
}
