package git

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// keepDaemonVars makes the git requests of one test keep the daemon's GIT_DIR
// and GIT_COMMON_DIR on Windows too, as while a request with "shadowDirOff":true
// runs (daemonVarsKept). A test of the rule of Linux and macOS calls it, so that
// it runs on Windows as it did before that rule.
func keepDaemonVars(t *testing.T) {
	t.Helper()
	offRequestsLive.Add(1)
	t.Cleanup(func() { offRequestsLive.Add(-1) })
}

// ignoredVarsRow sets the daemon's GIT_DIR and GIT_COMMON_DIR of one row for a
// repository with the git directory gitDir. other is the git directory of a
// second repository.
type ignoredVarsRow struct {
	name string
	set  func(t *testing.T, gitDir, other string)
}

// ignoredVarsRows are the rows of daemonVarsKept: a relative GIT_DIR, a GIT_DIR
// with a GIT_COMMON_DIR, a GIT_COMMON_DIR alone, of the repository or of
// another one, and each variable naming a missing path or a regular file.
func ignoredVarsRows() []ignoredVarsRow {
	file := func(t *testing.T) string {
		p := filepath.Join(t.TempDir(), "afile")
		writeFile(t, p, "x", 0o644)
		return p
	}
	return []ignoredVarsRow{
		{"relative GIT_DIR", func(t *testing.T, _, _ string) { t.Setenv("GIT_DIR", ".git") }},
		{"GIT_DIR and GIT_COMMON_DIR", func(t *testing.T, gitDir, _ string) {
			t.Setenv("GIT_DIR", gitDir)
			t.Setenv("GIT_COMMON_DIR", gitDir)
		}},
		{"GIT_COMMON_DIR", func(t *testing.T, gitDir, _ string) { t.Setenv("GIT_COMMON_DIR", gitDir) }},
		{"GIT_COMMON_DIR of another repository", func(t *testing.T, _, other string) { t.Setenv("GIT_COMMON_DIR", other) }},
		{"missing GIT_DIR", func(t *testing.T, _, _ string) { t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "gone")) }},
		{"file GIT_DIR", func(t *testing.T, _, _ string) { t.Setenv("GIT_DIR", file(t)) }},
		{"missing GIT_COMMON_DIR", func(t *testing.T, _, _ string) { t.Setenv("GIT_COMMON_DIR", filepath.Join(t.TempDir(), "gone")) }},
		{"file GIT_COMMON_DIR", func(t *testing.T, _, _ string) { t.Setenv("GIT_COMMON_DIR", file(t)) }},
	}
}

// ignoredVarsCalls runs skipRequests on a repository after a git.info on
// another one, with the daemon variables of set, or none with a nil set. It
// gives the replies and, per request, each call with its folder and its four
// folder variables. The paths of the run are replaced by fixed names.
func ignoredVarsCalls(t *testing.T, set func(t *testing.T, gitDir, other string)) (lists [][]string, replies []string) {
	t.Helper()
	f := newShadowFixture(t)
	setAttrSource(t, true)
	repo, gitDir := configRepo(t)
	other, otherGitDir := configRepo(t)
	if set != nil {
		set(t, gitDir, otherGitDir)
	}
	if raw, _ := f.info(t, other, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("the first git.info = %s", raw)
	}
	folder := daemonFolder(t, f.run)
	for _, m := range skipRequests(repo) {
		raw, calls := f.call(t, m.method, m.params)
		for _, old := range []string{strings.Trim(jsonQuoted(t, repo), `"`), filepath.ToSlash(repo), repo} {
			raw = strings.ReplaceAll(raw, old, "<repo>")
		}
		replies = append(replies, raw)
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

// TestIgnoredDaemonVarsMakeTheCallsOfNoVariable: with the Windows rule
// (copyPinsWalkGitDir) and no request with "shadowDirOff":true, git.info,
// git.status, a create and a remove make the calls of a daemon with no GIT_DIR and
// no GIT_COMMON_DIR, with the same folder variables and the same replies, in each
// row of ignoredVarsRows. git.info makes the call about the repository. 87375cc7
// does so on Windows (measured on Windows).
//
// Mutations: daemonCommonDirSet returns daemonCommonDirInEnv, daemonGitDirSkipped
// keeps its IsAbs test, gitDirTrustFor drops the walk for a verdict that is not
// trusted, and withoutIgnoredCommonDir returns env.
func TestIgnoredDaemonVarsMakeTheCallsOfNoVariable(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	var plain [][]string
	var plainReplies []string
	t.Run("no variable", func(t *testing.T) { plain, plainReplies = ignoredVarsCalls(t, nil) })
	if len(plain) != 4 {
		t.Fatalf("%d requests ran, want 4", len(plain))
	}
	// The object ids and the numbers of the short-lived folders differ per run.
	varying := regexp.MustCompile(`[0-9a-f]{40}|[0-9]+`)
	for _, row := range ignoredVarsRows() {
		t.Run(row.name, func(t *testing.T) {
			got, replies := ignoredVarsCalls(t, row.set)
			if !slices.Equal(plainReplies, replies) {
				t.Errorf("the replies differ:\n%q\n%q", plainReplies, replies)
			}
			if len(got) != len(plain) {
				t.Fatalf("%d requests ran, want %d", len(got), len(plain))
			}
			for i := range plain {
				a, b := varying.ReplaceAllString(strings.Join(plain[i], "\n"), "#"), varying.ReplaceAllString(strings.Join(got[i], "\n"), "#")
				if a != b {
					t.Errorf("request %d: %d calls with no variable and %d with the row\n%s\n---\n%s", i+1, len(plain[i]), len(got[i]), a, b)
				}
			}
			probes := 0
			for _, line := range got[0] {
				if strings.Contains(line, "rev-parse --git-dir ||") && !strings.Contains(line, "GIT_DIR=") {
					probes++
				}
			}
			if probes != 1 {
				t.Errorf("git.info made %d calls about the repository, want 1", probes)
			}
		})
	}
}

// TestIgnoredCommonDirKeptWithTheOffSwitch: while a request with
// "shadowDirOff":true runs, the Windows rule keeps the daemon's GIT_COMMON_DIR and
// its rule of a GIT_DIR, as 87375cc7 does (measured on Windows). Mutations:
// daemonVarsKept drops the count of offRequestsLive, and Handle drops its add.
func TestIgnoredCommonDirKeptWithTheOffSwitch(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	clearFolderEnv(t)
	dir := t.TempDir()
	t.Setenv("GIT_COMMON_DIR", dir)
	t.Setenv("GIT_DIR", ".git")
	has := func() bool {
		_, ok := lookupExact(gitChildEnv(), "GIT_COMMON_DIR")
		return ok
	}
	if daemonCommonDirSet() || has() || !daemonGitDirSkipped() {
		t.Errorf("no off switch: daemonCommonDirSet %v, GIT_COMMON_DIR in gitChildEnv %v, daemonGitDirSkipped %v, want false, false, true",
			daemonCommonDirSet(), has(), daemonGitDirSkipped())
	}
	t.Run("off switch", func(t *testing.T) {
		keepDaemonVars(t)
		if !daemonCommonDirSet() || !has() || daemonGitDirSkipped() {
			t.Errorf("daemonCommonDirSet %v, GIT_COMMON_DIR in gitChildEnv %v, daemonGitDirSkipped %v, want true, true, false",
				daemonCommonDirSet(), has(), daemonGitDirSkipped())
		}
	})
	// Handle counts a request with the switch while it runs.
	f := newShadowFixture(t)
	t.Setenv("GIT_COMMON_DIR", dir)
	repo, _ := configRepo(t)
	_, calls := f.info(t, repo, map[string]any{"shadowDirOff": true})
	listings := 0
	for _, c := range calls {
		if !c.listing() {
			continue
		}
		listings++
		if v, _ := lookupExact(c.env, "GIT_COMMON_DIR"); v != dir {
			t.Errorf("git.info with shadowDirOff: a listing has GIT_COMMON_DIR=%q, want %q", v, dir)
		}
	}
	if listings == 0 {
		t.Error("git.info with shadowDirOff made no listing")
	}
	if n := offRequestsLive.Load(); n != 0 {
		t.Errorf("offRequestsLive = %d after the request, want 0", n)
	}
}
