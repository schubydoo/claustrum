package git

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// git.info and git.list_branches on a path that is a Windows junction read the
// config through the target of the junction (configDirThroughJunction). The rows
// are those of 4c111e86 on Windows. A Windows test with real junctions is in
// gitinfo_junction_read_windows_test.go.

// standInJunction makes requestJunctionTarget answer target for the path link, as
// Windows answers for a junction there.
func standInJunction(t *testing.T, link, target string) {
	t.Helper()
	old := requestJunctionTarget
	t.Cleanup(func() { requestJunctionTarget = old })
	requestJunctionTarget = func(p string) (string, bool) {
		if p == link {
			return target, true
		}
		return "", false
	}
}

// brokenConfigRefusal is the refusal of a config with a bad line 6 in gitDir.
func brokenConfigRefusal(gitDir string) string {
	return repoConfigPrefix + "fatal: bad config line 6 in " + strconv.Quote(filepath.Join(gitDir, "config"))
}

// With a junction J to a repository T whose config has a bad line, the refusal
// names the config of T. The stand-in junction is a repository with a good
// config, so the old read of J answers normally. Mutation: in gitInfo or
// gitListBranches, drop the configDir line. In repoReadVerdict, read
// t.pinCommonDir in place of dir.
func TestConfigReadThroughRequestJunction(t *testing.T) {
	j, _ := configRepo(t)
	target, targetGit := configRepo(t)
	setRepoConfig(t, targetGit, "[core]\n\trepositoryformatversion = 0\n"+baseRepoConfig+"[x\n\tbroken\n = = =\n")
	standInJunction(t, j, target)
	want := brokenConfigRefusal(targetGit)
	wantRPCError(t, "info", info(t, j), want)
	wantRPCError(t, "list_branches", listBranches(t, j), want)
}

// A bare repository behind a junction passes the link test of the read, which
// tests the target. The pin is a link to the bare repository, and the read
// folder is the bare repository itself. Mutation: in repoReadVerdict, give
// gitDirLinkDetail t.pinCommonDir in place of dir.
func TestConfigReadDirTakesTheLinkTest(t *testing.T) {
	requireGit(t)
	base := resolveTestRoot(t, t.TempDir())
	bare := filepath.Join(base, "BARE")
	runGit(t, base, "init", "-q", "--bare", bare)
	jb := filepath.Join(base, "jb")
	if err := os.Symlink(bare, jb); err != nil {
		t.Skipf("symlink: %v", err)
	}
	linked := gitDirTrust{verdict: gitDirTrusted, pinCommonDir: jb}
	if v := repoReadVerdict(linked, nil); v == nil || v.refusal != repoConfigPrefix+gitDirLinkText(jb) {
		t.Fatalf("read of the link = %+v, want the link refusal", v)
	}
	linked.configDir = bare
	if v := repoReadVerdict(linked, nil); v == nil || v.refusal != "" {
		t.Errorf("read through the target = %+v, want no refusal", v)
	}
}

// configDirThroughJunction keeps the pin when the start is no junction, when the
// pin is outside the start, and with a daemon GIT_DIR. Mutation: drop the prefix
// test, or the GIT_DIR test.
func TestConfigDirThroughJunctionKeepsOtherPins(t *testing.T) {
	base := resolveTestRoot(t, t.TempDir())
	j := filepath.Join(base, "J")
	jx := filepath.Join(base, "Jx")
	for _, d := range []string{j, jx} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(base, "T")
	standInJunction(t, j, target)
	for _, tc := range []struct {
		cell, dir, pin, want string
	}{
		{"pin in the junction", j, filepath.Join(j, ".git"), filepath.Join(target, ".git")},
		{"pin is the junction", j, j, target},
		{"pin above the junction", j, filepath.Join(base, ".git"), ""},
		{"pin with the name of the junction as a prefix", j, jx, ""},
		{"no junction", jx, filepath.Join(jx, ".git"), ""},
		{"no pin", j, "", ""},
	} {
		if got := configDirThroughJunction(tc.dir, tc.pin); got != tc.want {
			t.Errorf("%s: read folder = %q, want %q", tc.cell, got, tc.want)
		}
	}
	t.Setenv("GIT_DIR", filepath.Join(j, ".git"))
	if got := configDirThroughJunction(j, filepath.Join(j, ".git")); got != "" {
		t.Errorf("daemon GIT_DIR: read folder = %q, want none", got)
	}
}
