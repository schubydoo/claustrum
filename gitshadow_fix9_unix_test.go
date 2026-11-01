//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// profileOf are the profile variables of a call, in the order of its
// environment.
func profileOf(c envCall) []string {
	var out []string
	for _, kv := range c.env {
		switch envName(kv) {
		case "GIT_ALLOW_PROTOCOL", "GIT_TERMINAL_PROMPT", "GIT_NO_REPLACE_OBJECTS", "GIT_GRAFT_FILE", "GIT_NO_LAZY_FETCH", "GIT_ASKPASS":
			out = append(out, kv)
		}
	}
	return out
}

// TestNoRepositoryCallsWithAnUnreadableHead: the HEAD of the repository has
// mode 000, so the trust check finds no repository. git.list_branches then
// makes the listing and the heavy `rev-parse --git-dir`. A create makes the
// listing and the light `rev-parse --is-inside-work-tree`, both with the six
// values of the state shadow in their order. Each call has
// GIT_DIR=<null device>. On 5fd08069 the two requests make 2 calls each, and
// the replies are the "no repository" answers (cell h1-mode000 of
// val-s3p2b2-7-linux2, Linux VM). The daemon has no state in that cell. With
// the member the create is not measured: its calls keep the four light values.
func TestNoRepositoryCallsWithAnUnreadableHead(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user reads a file of mode 000")
	}
	f := newShadowFixture(t)
	head := filepath.Join(f.gitDir, "HEAD")
	if err := os.Chmod(head, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(head, 0o644) })
	gitCalls := func(calls []envCall) []envCall {
		return slices.DeleteFunc(calls, func(c envCall) bool { return c.excludesRead() })
	}
	check := func(what string, calls []envCall, last []string, profile []string) {
		t.Helper()
		calls = gitCalls(calls)
		if len(calls) != 2 || !calls[0].listing() || !argvEndsWith(calls[1].argv, last) {
			t.Fatalf("%s made %d calls, want the listing and %q", what, len(calls), last)
		}
		for i, c := range calls {
			if v, ok := lookupExact(c.env, "GIT_DIR"); !ok || v != os.DevNull {
				t.Errorf("%s call %d: GIT_DIR = %q, %v, want the null device", what, i+1, v, ok)
			}
			if got := profileOf(c); !slices.Equal(got, profile) {
				t.Errorf("%s call %d: the profile variables are %q\nwant %q", what, i+1, got, profile)
			}
		}
	}
	six := []string{"GIT_ALLOW_PROTOCOL=denied_by_claude_ssh", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1",
		"GIT_GRAFT_FILE=" + os.DevNull, "GIT_NO_LAZY_FETCH=1", "GIT_ASKPASS="}

	raw, calls := f.call(t, "git.list_branches", map[string]any{"path": f.repo})
	if raw != `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"branches":[]}}` {
		t.Fatalf("git.list_branches = %s", raw)
	}
	check("git.list_branches", calls, []string{"rev-parse", "--git-dir"}, profileEnv(true))
	if v, _ := optionList(gitCalls(calls)[1].argv); len(v) != len(gitHardenHeavy) {
		t.Errorf("the rev-parse call of git.list_branches has %d items, want the heavy list of %d", len(v), len(gitHardenHeavy))
	}

	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	params := map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}
	raw, calls = f.call(t, "git.worktree_create", params)
	if raw != `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}` {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	check("git.worktree_create", calls, []string{"rev-parse", "--is-inside-work-tree"}, six)
	if v, _ := optionList(gitCalls(calls)[1].argv); len(v) == 0 || v[0] != gitHardenLight[0] {
		t.Errorf("the rev-parse call of the create starts its list with %q, want the light list", v)
	}

	params["shadowDirOff"] = true
	raw, calls = f.call(t, "git.worktree_create", params)
	if !strings.Contains(raw, `"errorCode":"not_a_repo"`) {
		t.Fatalf("git.worktree_create with the member = %s", raw)
	}
	check("git.worktree_create with the member", calls, []string{"rev-parse", "--is-inside-work-tree"}, profileEnv(false))
	if got := daemonFolders(t, f.run); len(got) != 0 {
		t.Errorf("git-shadow holds %v, want no folder of the daemon", got)
	}
}

// TestDaemonFolderThatTakesNoNewEntry: the folder of the daemon has mode 0500
// after the first request. git.status then answers isRepo:false, and a create
// answers the error of the mkdir with the full path of its index folder. On
// 5fd08069 the lines are `{"isRepo":false,"clean":false}` and `git worktree add
// failed (checkout): mkdir <S>/.index-<n>: permission denied` (cell f1-0500 of
// val-s3p2b2-7-linux2, Linux VM).
func TestDaemonFolderThatTakesNoNewEntry(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user makes an entry in a folder of mode 0500")
	}
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if raw, _ := f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo}); raw != entryStatusNoRepo {
		t.Errorf("git.status = %s\nwant %s", raw, entryStatusNoRepo)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	start := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add failed (checkout): mkdir ` +
		strings.Trim(jsonQuoted(t, filepath.Join(dir, shadowIndexDirPrefix)), `"`)
	rest, ok := strings.CutPrefix(raw, start)
	n, tail, _ := strings.Cut(rest, ":")
	if !ok || n == "" || strings.Trim(n, "0123456789") != "" || tail != ` permission denied","errorCode":"worktree_add_failed"}}` {
		t.Errorf("git.worktree_create = %s\nwant it to start %s<n>: permission denied", raw, start)
	}
}

// folderVars are the names of the folder variables of a call, in the order of
// its environment.
func folderVars(c envCall) []string {
	var out []string
	for _, kv := range c.env {
		switch n := envName(kv); n {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_WORK_TREE":
			out = append(out, n)
		}
	}
	return out
}

// TestDaemonGitDirStaysFirst: the daemon has a GIT_DIR of another repository
// in its environment. Each call that names the copy then has GIT_DIR, then
// GIT_COMMON_DIR=<copy>, then GIT_OBJECT_DIRECTORY, and no GIT_WORK_TREE. The
// copy is the one of the other repository. On 5fd08069 the 27 calls of the five
// requests have that order (cell o1-gd-v2 of val-s3p2b2-7-linux2, Linux VM).
func TestDaemonGitDirStaysFirst(t *testing.T) {
	f := newShadowFixture(t)
	_, otherGitDir := configRepo(t)
	t.Setenv("GIT_DIR", otherGitDir)
	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"isRepo":true`) || !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	copyOf := filepath.Join(daemonFolder(t, f.run), shadowRepoName(otherGitDir))
	if _, err := os.Lstat(filepath.Join(copyOf, "config")); err != nil {
		t.Fatalf("the copy of the other repository: %v", err)
	}
	named := 0
	for _, c := range calls {
		if v, _ := lookupExact(c.env, "GIT_COMMON_DIR"); v != copyOf {
			continue
		}
		named++
		if got, want := folderVars(c), []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY"}; !slices.Equal(got, want) {
			t.Errorf("the call %q has the folder variables %q, want %q", c.argv[len(c.argv)-2:], got, want)
		}
		if v, _ := lookupExact(c.env, "GIT_DIR"); v != otherGitDir {
			t.Errorf("GIT_DIR of the call = %s, want %s", v, otherGitDir)
		}
	}
	if named < 8 {
		t.Errorf("%d calls name the copy, want at least 8", named)
	}
}

// TestDaemonGitDirAndCommonDir: the daemon has GIT_DIR and GIT_COMMON_DIR of
// another repository in its environment. The five requests then make the calls
// that they make with GIT_DIR alone, and each call that names a copy has
// GIT_COMMON_DIR=<copy> in the place of the value of the daemon. On 5fd08069
// the call counts are 20, 2, 4, 11 and 2 in both cells, and the folder of the
// repository is made (cells o1-gd-v2 and o1-gdcd-v2 of val-s3p2b2-7-linux2,
// Linux VM).
func TestDaemonGitDirAndCommonDir(t *testing.T) {
	run := func(t *testing.T, both bool) (lists [][]string, replies []string) {
		f := newShadowFixture(t)
		// The probe of --attr-source counts as done, so that neither cell
		// makes it and the lists stay comparable.
		setAttrSource(t, true)
		_, otherGitDir := configRepo(t)
		t.Setenv("GIT_DIR", otherGitDir)
		if both {
			t.Setenv("GIT_COMMON_DIR", otherGitDir)
		}
		copyOf := ""
		for _, m := range f.fiveRequests(nil) {
			raw, calls := f.call(t, m.method, m.params)
			replies = append(replies, strings.ReplaceAll(raw, f.repo, "<repo>"))
			if copyOf == "" {
				copyOf = filepath.Join(daemonFolder(t, f.run), shadowRepoName(otherGitDir))
				if _, err := os.Lstat(filepath.Join(copyOf, "config")); err != nil {
					t.Fatalf("the copy of the other repository after %s: %v", m.method, err)
				}
			}
			var list []string
			for _, c := range calls {
				line := strings.Join(c.argv, " ")
				for _, old := range []string{filepath.Dir(copyOf), f.run, f.repo, otherGitDir} {
					line = strings.ReplaceAll(line, old, "<x>")
				}
				common, _ := lookupExact(c.env, "GIT_COMMON_DIR")
				// The pair of a remove carries the copy: on 5fd08069 the
				// calls 3 to 6 of a second remove do (cell N-g of
				// val-s3p2b2-8-linux2, Linux VM).
				switch {
				case common == copyOf:
					line += " [copy]"
				case strings.Contains(common, ".check-"), common == "" || c.configRead() || c.excludesRead():
				default:
					t.Errorf("%s: the call %q has GIT_COMMON_DIR=%s, want the copy %s", m.method, c.argv[len(c.argv)-2:], common, copyOf)
				}
				list = append(list, line)
			}
			lists = append(lists, list)
		}
		return lists, replies
	}
	var alone, both [][]string
	var aloneReplies, bothReplies []string
	t.Run("GIT_DIR", func(t *testing.T) { alone, aloneReplies = run(t, false) })
	t.Run("GIT_DIR and GIT_COMMON_DIR", func(t *testing.T) { both, bothReplies = run(t, true) })
	if len(alone) != 5 || len(both) != 5 {
		t.Fatalf("%d and %d requests ran, want 5 and 5", len(alone), len(both))
	}
	if !slices.Equal(aloneReplies, bothReplies) {
		t.Errorf("the replies differ:\n%q\n%q", aloneReplies, bothReplies)
	}
	// The object ids and the numbers of the short-lived folders differ per run.
	varying := regexp.MustCompile(`[0-9a-f]{40}|[0-9]+`)
	for i := range alone {
		a, b := varying.ReplaceAllString(strings.Join(alone[i], "\n"), "#"), varying.ReplaceAllString(strings.Join(both[i], "\n"), "#")
		if a != b {
			t.Errorf("request %d: %d calls with GIT_DIR alone and %d with both variables\n%s\n---\n%s", i+1, len(alone[i]), len(both[i]), a, b)
		}
	}
	// 5fd08069 makes 20, 2, 4 and 11 calls in the first four requests of both
	// cells. The git directory of the daemon has no registrations folder, so
	// the create runs no pair before the add (pairBeforeAdd). 683cc6a9 adds the
	// settle call after the checkout (worktreesettle.go). be1c1ca5 adds the
	// config read of the ssh door before it, so the create makes 13 (gitsshdoor.go).
	if got := []int{len(both[0]), len(both[1]), len(both[2]), len(both[3])}; !slices.Equal(got, []int{20, 2, 4, 13}) {
		t.Errorf("the first four requests made %v calls, want 20, 2, 4 and 13", got)
	}
}

// TestFailedInitNamesTheGitVersion: the first call of the start test exits 1
// with a text. The cause then ends with the answer of `git version` in
// brackets, after two version calls: one in the root folder, and one with the
// form of a test call. On 5fd08069 the text is `git init failed under "<S>":
// fatal: forced failure by the test wrapper (git version 2.43.0)`, and the
// request makes 4 git calls (cell C2 of val-s3p2b2-7-linux2, Linux VM).
func TestFailedInitNamesTheGitVersion(t *testing.T) {
	out, err := exec.Command("git", "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(out))
	f := newShadowFixture(t)
	slowGit(t, "init", "fail", 0, "fatal: forced failure by the test wrapper\n", "")
	raw, calls := f.info(t, f.repo, nil)
	dir := daemonFolder(t, f.run)
	want := jsonQuoted(t, failedTestText("git init failed under "+strconv.Quote(dir)+": fatal: forced failure by the test wrapper ("+version+")"))
	if !strings.Contains(raw, `"message":`+want) {
		t.Errorf("git.info = %s\nwant the message %s", raw, want)
	}
	if len(calls) != 4 || !calls[0].excludesRead() || !slices.Contains(calls[1].argv, "init") {
		t.Fatalf("%d calls, want the excludes read, the init and two version calls", len(calls))
	}
	plain, inTest := calls[2], calls[3]
	if !slices.Equal(plain.argv, []string{"version"}) || plain.cwd != "/" {
		t.Errorf("the first version call = %q in %s, want it plain and in the root folder", plain.argv, plain.cwd)
	}
	if inTest.argv[len(inTest.argv)-1] != "version" || filepath.Dir(inTest.cwd) != dir || inTest.env[0] != "GIT_CONFIG_NOSYSTEM=1" {
		t.Errorf("the second version call = %q in %s, want the form of a test call", inTest.argv, inTest.cwd)
	}
	if got := entryNames(t, dir); !slices.Equal(got, []string{shadowLockName}) {
		t.Errorf("the folder of the daemon after the failed test holds %v, want the lock file only", got)
	}
}
