package git

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// A linked worktree whose `.git` file names its entry through a junction reads the
// config at the target of the junction (entryRepoThroughJunction). The rows are those
// of 4c111e86 and 87375cc7 on Windows. A Windows test with real junctions is in
// gitinfo_junction_read_windows_test.go.

// brokenConfigRefusal is the refusal of a config with a bad line 6 in gitDir.
func brokenConfigRefusal(gitDir string) string {
	return repoConfigPrefix + "fatal: bad config line 6 in " + strconv.Quote(filepath.Join(gitDir, "config"))
}

// The kept read has the folder of the read as its key, so one pin with two read
// folders reads twice, and a bad config at the second folder is refused. Mutation: in
// repoReadVerdict, key the kept read by t.pinCommonDir, or read t.pinCommonDir in
// place of dir.
func TestConfigReadTakesTheReadFolder(t *testing.T) {
	_, pinned := configRepo(t)
	_, good := configRepo(t)
	_, targetGit := configRepo(t)
	setRepoConfig(t, targetGit, "[core]\n\trepositoryformatversion = 0\n"+baseRepoConfig+"[x\n\tbroken\n = = =\n")
	if v := repoReadVerdict(gitDirTrust{verdict: gitDirTrusted, pinCommonDir: pinned, configDir: good}, nil); v == nil || v.refusal != "" {
		t.Fatalf("read of the good folder = %+v", v)
	}
	v := repoReadVerdict(gitDirTrust{verdict: gitDirTrusted, pinCommonDir: pinned, configDir: targetGit}, nil)
	if want := brokenConfigRefusal(targetGit); v == nil || v.refusal != want {
		t.Errorf("read of the bad folder = %+v, want %q", v, want)
	}
}

// The `.git` file of a linked worktree names its entry through a junction J. The pin
// keeps J, and the read and the hold take the target. A repository folder named
// `.git` and a daemon GIT_DIR keep the pin alone. Mutation: in
// entryRepoThroughJunction, drop the test of the name `.git`, or of fromEnv. In
// judgeGitDir, drop the configDir member.
func TestEntryRepoThroughJunction(t *testing.T) {
	requireGit(t)
	base := resolveTestRoot(t, t.TempDir())
	j := filepath.Join(base, "J")
	dotGit := filepath.Join(base, "R", ".git")
	target := filepath.Join(base, "T")
	for _, repo := range []string{j, dotGit} {
		runGit(t, base, "init", "-q", "--bare", repo)
		entry := filepath.Join(repo, "worktrees", "wl")
		writeFile(t, filepath.Join(entry, "commondir"), "../..\n", 0o644)
		writeFile(t, filepath.Join(entry, "HEAD"), "ref: refs/heads/main\n", 0o644)
	}
	old := requestJunctionTarget
	t.Cleanup(func() { requestJunctionTarget = old })
	requestJunctionTarget = func(string) (string, bool) { return target, true }
	got := judgeGitDir(filepath.Join(j, "worktrees", "wl"), false)
	if got.pinCommonDir != j || got.configDir != target || got.holdFolderFor(j) != target {
		t.Errorf("through a junction: pin %q, read %q, hold %q", got.pinCommonDir, got.configDir, got.holdFolderFor(j))
	}
	if got := judgeGitDir(filepath.Join(dotGit, "worktrees", "wl"), false); got.configDir != "" {
		t.Errorf("a folder named .git: read %q, want none", got.configDir)
	}
	if got := judgeGitDir(filepath.Join(j, "worktrees", "wl"), true); got.configDir != "" {
		t.Errorf("a daemon GIT_DIR: read %q, want none", got.configDir)
	}
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
