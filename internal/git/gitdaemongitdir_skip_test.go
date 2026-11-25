package git

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// setCopyPinsWalkGitDir sets copyPinsWalkGitDir for one test.
func setCopyPinsWalkGitDir(t *testing.T, on bool) {
	t.Helper()
	old := copyPinsWalkGitDir
	copyPinsWalkGitDir = on
	t.Cleanup(func() { copyPinsWalkGitDir = old })
}

// skipRequests are the four requests of the rows of daemonGitDirSkipped on repo:
// git.info, git.status, a create of w1 and its remove.
func skipRequests(repo string) []struct {
	method string
	params map[string]any
} {
	leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
	return []struct {
		method string
		params map[string]any
	}{
		{"git.info", map[string]any{"path": repo}},
		{"git.status", map[string]any{"path": repo, "baseRepo": repo}},
		{"git.worktree_create", map[string]any{"baseRepo": repo, "branchName": "w1", "worktreePath": leaf}},
		{"git.worktree_remove", map[string]any{"baseRepo": repo, "worktreePath": leaf, "branchName": "w1"}},
	}
}

// TestDaemonGitDirSkippedMakesTheCallsOfNoGitDir: with daemonGitDirSkipped and a
// daemon GIT_DIR of the repository's own `.git`, git.info, git.status, a create
// and a remove make the calls that they make with no daemon GIT_DIR, with the
// same folder variables. A git.info on another repository comes first. So
// git.info makes the call about the repository and the read of the config, and
// the create asks git again. 87375cc7 has these calls on Windows (measured on
// Windows). The replies are equal too.
//
// Mutations: in asksGit drop `|| daemonGitDirSkipped()` (no call about the
// repository), in the settings gate drop copyTrust (no read of the config in
// git.info), in gitUse.pin drop `&& !daemonGitDirSkipped()` (no GIT_DIR and
// GIT_WORK_TREE), in daemonPartFor return daemon (GIT_DIR on the calls with
// --git-dir=), and in probeEnv drop withoutSkippedGitDir.
func TestDaemonGitDirSkippedMakesTheCallsOfNoGitDir(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	run := func(t *testing.T, withGitDir bool) (lists [][]string, replies []string) {
		f := newShadowFixture(t)
		setAttrSource(t, true)
		repo, gitDir := configRepo(t)
		if withGitDir {
			t.Setenv("GIT_DIR", gitDir)
		}
		other, _ := configRepo(t)
		if raw, _ := f.info(t, other, nil); !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("the first git.info = %s", raw)
		}
		folder := daemonFolder(t, f.run)
		for _, m := range skipRequests(repo) {
			raw, calls := f.call(t, m.method, m.params)
			replies = append(replies, strings.ReplaceAll(raw, repo, "<repo>"))
			var list []string
			for _, c := range calls {
				line := c.cwd + " | " + strings.Join(dropConfigArgs(c.argv), " ") + " ||"
				for _, name := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_WORK_TREE"} {
					if v, ok := lookupExact(c.env, name); ok {
						line += " " + name + "=" + v
					}
				}
				for _, old := range []string{filepath.Join(folder, shadowRepoName(gitDir)), folder, f.run, repo} {
					line = strings.ReplaceAll(line, old, "<x>")
				}
				list = append(list, line)
			}
			lists = append(lists, list)
		}
		return lists, replies
	}
	var plain, skipped [][]string
	var plainReplies, skippedReplies []string
	t.Run("no daemon GIT_DIR", func(t *testing.T) { plain, plainReplies = run(t, false) })
	t.Run("daemon GIT_DIR", func(t *testing.T) { skipped, skippedReplies = run(t, true) })
	if len(plain) != 4 || len(skipped) != 4 {
		t.Fatalf("%d and %d requests ran, want 4 and 4", len(plain), len(skipped))
	}
	if !slices.Equal(plainReplies, skippedReplies) {
		t.Errorf("the replies differ:\n%q\n%q", plainReplies, skippedReplies)
	}
	// The object ids and the numbers of the short-lived folders differ per run.
	varying := regexp.MustCompile(`[0-9a-f]{40}|[0-9]+`)
	for i := range plain {
		a, b := varying.ReplaceAllString(strings.Join(plain[i], "\n"), "#"), varying.ReplaceAllString(strings.Join(skipped[i], "\n"), "#")
		if a != b {
			t.Errorf("request %d: %d calls with no daemon GIT_DIR and %d with one\n%s\n---\n%s", i+1, len(plain[i]), len(skipped[i]), a, b)
		}
	}
	probes := 0
	for _, line := range skipped[0] {
		if strings.Contains(line, "rev-parse --git-dir ||") && !strings.Contains(line, "GIT_DIR=") {
			probes++
		}
	}
	if probes != 1 {
		t.Errorf("git.info made %d calls about the repository, want 1:\n%s", probes, strings.Join(skipped[0], "\n"))
	}
}

// dropConfigArgs is argv without its `-c <item>` pairs.
func dropConfigArgs(argv []string) []string {
	var out []string
	for i := 0; i < len(argv); i++ {
		if argv[i] == "-c" {
			i++
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

// TestDaemonGitDirSkippedPin: with daemonGitDirSkipped a call in the repository
// gets GIT_DIR and GIT_WORK_TREE of the walk after the two variables of the
// copy, and a call with --git-dir= the two variables only. The daemon's own
// GIT_DIR goes from the environment of both. With no copy it stays. Mutations:
// in gitUse.pin drop `&& !daemonGitDirSkipped()`, and in daemonPartFor return
// daemon. 87375cc7 has these variables on Windows (measured on Windows).
func TestDaemonGitDirSkippedPin(t *testing.T) {
	clearFolderEnv(t)
	f := newRepoFolderFixture(t)
	u := f.useOf(t, f.repo)
	common := u.commonVars()
	full := append(slices.Clone(common), "GIT_DIR="+f.gitDir, "GIT_WORK_TREE="+f.repo)
	t.Setenv("GIT_DIR", f.gitDir)
	setCopyPinsWalkGitDir(t, true)
	if got := u.pin(f.repo); !slices.Equal(got, full) {
		t.Errorf("pin = %q\nwant %q", got, full)
	}
	daemon := []string{"A=1", "GIT_DIR=" + f.gitDir, "B=2"}
	for _, pin := range [][]string{u.pin(f.repo), u.gitDirPin(f.repo)} {
		if got, want := daemonPartFor(daemon, pin), []string{"A=1", "B=2"}; !slices.Equal(got, want) {
			t.Errorf("daemonPartFor(%q) = %q, want %q", pin, got, want)
		}
	}
	if got := daemonPartFor(daemon, []string{"GIT_COMMON_DIR=" + f.gitDir}); !slices.Equal(got, daemon) {
		t.Errorf("no copy: daemonPartFor = %q, want %q", got, daemon)
	}
	// A daemon GIT_DIR of another repository leaves the copy that of the walk
	// (claustrum's own, not measured). Mutation: in gitUse.namesCopy drop copyTrust.
	_, otherGitDir := configRepo(t)
	t.Setenv("GIT_DIR", otherGitDir)
	if got := u.pin(f.repo); !slices.Equal(got, full) {
		t.Errorf("GIT_DIR of another repository: pin = %q\nwant %q", got, full)
	}
	t.Setenv("GIT_DIR", f.gitDir)
	setCopyPinsWalkGitDir(t, false)
	if got := u.pin(f.repo); !slices.Equal(got, common) {
		t.Errorf("off: pin = %q\nwant %q", got, common)
	}
	if got := daemonPartFor(daemon, u.gitDirPin(f.repo)); !slices.Equal(got, daemon) {
		t.Errorf("off: daemonPartFor = %q, want %q", got, daemon)
	}
}

// TestDaemonGitDirSkippedProbes: with daemonGitDirSkipped the call about the
// repository and the `--attr-source` probe get no daemon GIT_DIR. Mutations:
// drop withoutSkippedGitDir in probeEnv, and in attrProbeCmd.
func TestDaemonGitDirSkippedProbes(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	t.Setenv("GIT_DIR", gitDir)
	has := func(env []string) bool {
		_, ok := lookupExact(env, "GIT_DIR")
		return ok
	}
	setCopyPinsWalkGitDir(t, true)
	if env := probeEnv(t.TempDir(), t.TempDir()); has(env) {
		t.Errorf("probeEnv has GIT_DIR: %q", env)
	}
	if env := attrProbeCmd("version").Env; has(env) {
		t.Errorf("the --attr-source probe has GIT_DIR: %q", env)
	}
	setCopyPinsWalkGitDir(t, false)
	if env := attrProbeCmd("version").Env; !has(env) {
		t.Errorf("off: the --attr-source probe has no GIT_DIR: %q", env)
	}
	if v, _ := lookupExact(os.Environ(), "GIT_DIR"); v != gitDir {
		t.Fatalf("GIT_DIR = %q", v)
	}
}
