package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests of the last steps of the start test. Each test stages the answer
// of one or two test calls in the git stub, and no real git runs for a staged
// call. Each names the cell of round tier0-5 (Linux VM) whose reply of 5fd08069
// it follows.

// stagedCall is one staged answer of the git stub. Match are exact argv words.
// Copy "yes" takes only a call with a GIT_COMMON_DIR, and "no" only a call
// without one. Nth takes only the nth such call of the stub, counted from 1,
// and 0 takes each. A call counts for every entry that it matches, and the
// first entry whose count fits gives the answer. The call writes Stdout and Stderr and exits with Exit.
// With Kill it ends itself with a kill.
type stagedCall struct {
	Match  []string
	Copy   string
	Nth    int
	Exit   int
	Kill   bool
	Stdout string
	Stderr string
}

// stageGit gives the git stub the staged answers. A second call starts the
// counts of Nth again.
func stageGit(t *testing.T, calls ...stagedCall) {
	t.Helper()
	b, err := json.Marshal(calls)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "stage.json")
	if err := os.WriteFile(file, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUSTRUM_GITSTUB_STAGE", file)
}

// stagedGitAnswer is the stub side of stageGit: the exit code of the staged
// answer for args, when one matches.
func stagedGitAnswer(args []string) (int, bool) {
	file := os.Getenv("CLAUSTRUM_GITSTUB_STAGE")
	if file == "" {
		return 0, false
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return 0, false
	}
	var calls []stagedCall
	if json.Unmarshal(b, &calls) != nil {
		return 0, false
	}
	_, onCopy := os.LookupEnv("GIT_COMMON_DIR")
	chosen := -1
	for i, c := range calls {
		if !stubMatches(args, strings.Join(c.Match, ",")) || (c.Copy == "yes" && !onCopy) || (c.Copy == "no" && onCopy) {
			continue
		}
		if c.Nth != 0 {
			count := file + "." + strconv.Itoa(i)
			appendLine(count, "")
			if seen, _ := os.ReadFile(count); bytes.Count(seen, []byte("\n")) != c.Nth {
				continue
			}
		}
		if chosen < 0 {
			chosen = i
		}
	}
	if chosen >= 0 {
		c := calls[chosen]
		_, _ = os.Stdout.WriteString(c.Stdout)
		_, _ = os.Stderr.WriteString(c.Stderr)
		if c.Kill {
			if p, err := os.FindProcess(os.Getpid()); err == nil {
				_ = p.Kill()
				time.Sleep(time.Minute)
			}
		}
		return c.Exit, true
	}
	return 0, false
}

var (
	stageGet    = []string{"config", "--get", "helper.check"}
	stageVerify = []string{"rev-parse", "--verify"}
	stageAdd    = []string{"worktree", "add"}
)

// Two staged answers for one call, the first and the second such call: each
// call gets its own answer. Neither call runs a real git.
func TestShadowStagedCallsCountForEveryEntry(t *testing.T) {
	newShadowFixture(t)
	word := []string{"seam-check"}
	stageGit(t,
		stagedCall{Match: word, Nth: 1, Stdout: "one"},
		stagedCall{Match: word, Nth: 2, Stdout: "two"})
	for _, want := range []string{"one", "two"} {
		out, err := exec.Command("git", word...).Output()
		if err != nil || string(out) != want {
			t.Errorf("staged call = %q, %v, want %q", out, err, want)
		}
	}
}

// startTestCalls are the calls of the start test in calls, the calls of one
// request: all after the excludes read of a first request, up to the last one
// that names a .check- folder. Call 7 names none and is between them. after
// is the number of calls of the request that follow.
func startTestCalls(t *testing.T, calls []envCall) (test []envCall, after int) {
	t.Helper()
	if len(calls) > 0 && calls[0].excludesRead() {
		calls = calls[1:]
	}
	last := -1
	for i, c := range calls {
		if len(testCalls([]envCall{c})) == 1 {
			last = i
		}
	}
	return calls[:last+1], len(calls) - 1 - last
}

// wantLastTestCall fails unless the test made n calls and its last one has the
// words and is on the copy, or on `real` alone.
func wantLastTestCall(t *testing.T, test []envCall, n int, onCopy bool, words ...string) {
	t.Helper()
	if len(test) != n {
		t.Fatalf("the test made %d calls, want %d", len(test), n)
	}
	last := test[n-1]
	_, copied := lookupExact(last.env, "GIT_COMMON_DIR")
	_, objects := lookupExact(last.env, "GIT_OBJECT_DIRECTORY")
	_, gitDir := lookupExact(last.env, "GIT_DIR")
	_, workTree := lookupExact(last.env, "GIT_WORK_TREE")
	if !stubMatches(last.argv, strings.Join(words, ",")) || copied != onCopy || objects != onCopy || !gitDir || !workTree {
		t.Errorf("the last test call = %v (on the copy: %v)\nwant %v (on the copy: %v)", last.argv, copied, words, onCopy)
	}
}

// wantFallback fails unless raw is the git.info reply of the state
// fallback-git and the folder of the daemon holds the lock file only.
func wantFallback(t *testing.T, f shadowFixture, raw string) {
	t.Helper()
	if !strings.Contains(raw, `"isRepo":true`) || !strings.HasSuffix(raw, `"settingsControl":"fallback-git"}}`) {
		t.Errorf("git.info = %s\nwant the state fallback-git", raw)
	}
	if got := entryNames(t, daemonFolder(t, f.run)); !slices.Equal(got, []string{shadowLockName}) {
		t.Errorf("folder of the daemon holds %v, want the lock file only", got)
	}
}

// wantFailedTest fails unless raw is the error of a failed start test with
// cause after the folder of the daemon, no call followed the test, and the
// folder of the daemon holds the lock file only.
func wantFailedTest(t *testing.T, f shadowFixture, raw string, after int, verb, cause string) {
	t.Helper()
	dir := daemonFolder(t, f.run)
	text := failedTestText("git " + verb + " failed under " + strconv.Quote(dir) + ": " + cause)
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` + jsonQuoted(t, text) + `}}`
	if raw != want {
		t.Errorf("git.info = %s\nwant %s", raw, want)
	}
	if after != 0 {
		t.Errorf("%d calls after the failed test, want none", after)
	}
	if got := entryNames(t, dir); !slices.Equal(got, []string{shadowLockName}) {
		t.Errorf("folder of the daemon holds %v, want the lock file only", got)
	}
}

// A first `config --get` that exits 1 with no output does not stop the
// test. 5fd08069 runs all ten calls and the state is shadow (cell C3 of round tier0-5).
func TestShadowQuietFirstGetGoesOn(t *testing.T) {
	f := newShadowFixture(t)
	stageGit(t, stagedCall{Match: stageGet, Copy: "yes", Nth: 1, Exit: 1})
	raw, calls := f.info(t, f.repo, nil)
	if !strings.HasSuffix(raw, `"settingsControl":"shadow"}}`) {
		t.Errorf("git.info = %s\nwant the state shadow", raw)
	}
	test, _ := startTestCalls(t, calls)
	wantLastTestCall(t, test, 10, true, stageAdd...)
	if got := entryNames(t, daemonFolder(t, f.run)); slices.ContainsFunc(got, func(n string) bool { return strings.HasPrefix(n, ".check-") }) {
		t.Errorf("folder of the daemon holds %v, want no .check- folder", got)
	}
}

// A first `config --get` that works and prints `real` gives the state
// fallback-git at once. It is the last of 6 test calls on 5fd08069 (cell C3b of round tier0-5).
func TestShadowFirstGetThatPrintsRealGivesFallback(t *testing.T) {
	f := newShadowFixture(t)
	stageGit(t, stagedCall{Match: stageGet, Copy: "yes", Nth: 1, Stdout: "real\n"})
	raw, calls := f.info(t, f.repo, nil)
	wantFallback(t, f, raw)
	test, _ := startTestCalls(t, calls)
	wantLastTestCall(t, test, 6, true, stageGet...)
}

// A second `config --get` that works and prints another text gives the
// state fallback-git. It is the last of 8 test calls on 5fd08069 (cell C4 of
// round tier0-5, the part with the text `x`).
func TestShadowSecondGetWithAnotherTextGivesFallback(t *testing.T) {
	f := newShadowFixture(t)
	stageGit(t, stagedCall{Match: stageGet, Copy: "yes", Nth: 2, Stdout: "x\n"})
	raw, calls := f.info(t, f.repo, nil)
	wantFallback(t, f, raw)
	test, _ := startTestCalls(t, calls)
	wantLastTestCall(t, test, 8, true, stageGet...)
}

// After a second `config --get` that exits 1, one more `config --get`
// runs on `real` alone, and the state is fallback-git. 5fd08069 makes 9 test
// calls (cell C4 of round tier0-5, the part with exit 1).
func TestShadowFailedSecondGetRunsOneGetOnRealAlone(t *testing.T) {
	f := newShadowFixture(t)
	stageGit(t, stagedCall{Match: stageGet, Copy: "yes", Nth: 2, Exit: 1})
	raw, calls := f.info(t, f.repo, nil)
	wantFallback(t, f, raw)
	test, _ := startTestCalls(t, calls)
	wantLastTestCall(t, test, 9, false, stageGet...)
}

// A `rev-parse` that works and prints another id gives the state
// fallback-git with no second call. 5fd08069 makes 9 test calls (cell C2 of round tier0-5).
func TestShadowRevParseWithAnotherIDGivesFallback(t *testing.T) {
	f := newShadowFixture(t)
	stageGit(t, stagedCall{Match: stageVerify, Copy: "yes", Stdout: strings.Repeat("0", 40) + "\n"})
	raw, calls := f.info(t, f.repo, nil)
	wantFallback(t, f, raw)
	test, _ := startTestCalls(t, calls)
	wantLastTestCall(t, test, 9, true, stageVerify...)
}

// A `rev-parse` that exits 128 with one error line, with a second call
// that works, gives the state fallback-git. 5fd08069 makes 10 test calls and
// no add (cell C1 of round tier0-5).
func TestShadowRevParseWithAnErrorLineGivesFallback(t *testing.T) {
	f := newShadowFixture(t)
	stageGit(t, stagedCall{Match: stageVerify, Copy: "yes", Exit: 128, Stderr: "fatal: forced rev-parse failure\n"})
	raw, calls := f.info(t, f.repo, nil)
	wantFallback(t, f, raw)
	test, _ := startTestCalls(t, calls)
	wantLastTestCall(t, test, 10, false, stageVerify...)
}

// When both `rev-parse` calls fail, the cause names the first one. On
// 5fd08069 the first exits 1 with no output and the second exits 128 with a
// line, and the cause ends with `exit status 1` after 10 test calls (cell C9 of round tier0-5).
func TestShadowTwoFailedRevParseCallsNameTheFirst(t *testing.T) {
	f := newShadowFixture(t)
	stageGit(t,
		stagedCall{Match: stageVerify, Copy: "yes", Exit: 1},
		stagedCall{Match: stageVerify, Copy: "no", Exit: 128, Stderr: "fatal: forced second rev-parse failure\n"})
	raw, calls := f.info(t, f.repo, nil)
	test, after := startTestCalls(t, calls)
	wantFailedTest(t, f, raw, after, "rev-parse", "exit status 1")
	wantLastTestCall(t, test, 10, false, stageVerify...)
}

// An add that exits 0 and makes nothing gives the state fallback-git with
// no add of `wt2`. 5fd08069 makes 10 test calls (cell C5 of round tier0-5).
func TestShadowAddThatMakesNothingGivesFallback(t *testing.T) {
	f := newShadowFixture(t)
	stageGit(t, stagedCall{Match: stageAdd, Copy: "yes"})
	raw, calls := f.info(t, f.repo, nil)
	wantFallback(t, f, raw)
	test, _ := startTestCalls(t, calls)
	wantLastTestCall(t, test, 10, true, stageAdd...)
}

// An add that exits 128 with `fatal: forced failure`, with an add of
// `wt2` that works, gives the state fallback-git after 11 test calls on
// 5fd08069 (cell N1-control of round tier0-5).
func TestShadowRefusedAddWithAGoodSecondAddGivesFallback(t *testing.T) {
	f := newShadowFixture(t)
	stageGit(t, stagedCall{Match: stageAdd, Copy: "yes", Exit: 128, Stderr: "fatal: forced failure\n"})
	raw, calls := f.info(t, f.repo, nil)
	wantFallback(t, f, raw)
	test, _ := startTestCalls(t, calls)
	wantLastTestCall(t, test, 11, false, stageAdd...)
	if leaf := test[10].argv[len(test[10].argv)-2]; filepath.Base(leaf) != "wt2" {
		t.Errorf("the second add names %s, want wt2", leaf)
	}
}

// An add whose error text holds `No space left on device` fails the
// test, also when the add of `wt2` works. The cause has the error text of the
// first add, 11 test calls ran, and the request after the kept time runs the
// whole test again. 5fd08069 answers so for a text of one line (cell N1 of round tier0-5) and
// for the two lines of git on a full disk (cell N2 of round tier0-5).
func TestShadowAddWithNoSpaceLeftFailsTheTest(t *testing.T) {
	for name, c := range map[string]struct{ stderr, cause string }{
		"one line (N1)": {
			"fatal: could not create directory: No space left on device\n",
			"fatal: could not create directory: No space left on device",
		},
		"two lines (N2)": {
			"Preparing worktree (detached HEAD 7e08a7b)\nfatal: could not write to 'wt/locked': No space left on device\n",
			"Preparing worktree (detached HEAD 7e08a7b) fatal: could not write to 'wt/locked': No space left on device",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newShadowFixture(t)
			now := useShadowClock(t)
			logs := captureLogBuf(t)
			stageGit(t, stagedCall{Match: stageAdd, Copy: "yes", Exit: 128, Stderr: c.stderr})
			for range 2 {
				raw, calls := f.info(t, f.repo, nil)
				test, after := startTestCalls(t, calls)
				wantFailedTest(t, f, raw, after, "worktree", c.cause)
				wantLastTestCall(t, test, 11, false, stageAdd...)
				*now = now.Add(shadowFailureKeep)
			}
			if n := stateLines(logs.String()); n != 0 {
				t.Errorf("%d state lines after a failed test, want none", n)
			}
		})
	}
}

// The cause of a failed `mktree` has both of its error lines, joined by
// one space, and the version text. After the failed call two `version` calls
// run: one with no option, and one with the options and in the folder of a
// test call. 5fd08069 makes those 5 test calls (cell C6 of round tier0-5).
func TestShadowFailedMktreeHasAllLinesAndTheVersion(t *testing.T) {
	f := newShadowFixture(t)
	stageGit(t,
		stagedCall{Match: []string{"mktree"}, Exit: 1, Stderr: "error: first forced line\nfatal: second forced line\n"},
		stagedCall{Match: []string{"version"}, Stdout: "git version 2.43.0\n"})
	raw, calls := f.info(t, f.repo, nil)
	test, after := startTestCalls(t, calls)
	wantFailedTest(t, f, raw, after, "mktree", "error: first forced line fatal: second forced line (git version 2.43.0)")
	if len(test) != 5 {
		t.Fatalf("the test made %d calls, want 5", len(test))
	}
	plain, inTest := test[3], test[4]
	if !slices.Equal(plain.argv, []string{"version"}) || strings.Contains(plain.cwd, ".check-") {
		t.Errorf("first version call = %v in %s, want no option and not the folder of the test", plain.argv, plain.cwd)
	}
	if _, ok := lookupExact(plain.env, "GIT_AUTHOR_NAME"); ok {
		t.Error("the first version call has the variables of the test")
	}
	if !slices.Equal(inTest.argv, append(shadowTestPrefix(), "version")) || inTest.cwd != test[0].cwd {
		t.Errorf("second version call = %v in %s, want the options and the folder %s", inTest.argv, inTest.cwd, test[0].cwd)
	}
	_, folder := lookupExact(inTest.env, "GIT_DIR")
	if v, ok := lookupExact(inTest.env, "GIT_CONFIG_NOSYSTEM"); folder || !ok || v != "1" {
		t.Errorf("second version call: GIT_DIR set = %v, GIT_CONFIG_NOSYSTEM = %q, want the variables of a test call with no folder", folder, v)
	}
}

// A failed `config`, `commit-tree` or `update-ref` call of the calls 1 to 5
// has the cause of a failed `mktree`: its verb, every error line, and the
// version text after two `version` calls. No cell has such a call. The test
// pins claustrum's own choice, not a reply of 5fd08069.
func TestShadowFailedSetupCallsShareThePathOfMktree(t *testing.T) {
	for _, c := range []struct {
		verb  string
		match []string
		calls int
	}{
		{"config", []string{"config", "helper.check", "real"}, 4},
		{"commit-tree", []string{"commit-tree"}, 6},
		{"update-ref", []string{"update-ref"}, 7},
	} {
		t.Run(c.verb, func(t *testing.T) {
			f := newShadowFixture(t)
			stageGit(t,
				stagedCall{Match: c.match, Exit: 1, Stderr: "error: first forced line\nfatal: second forced line\n"},
				stagedCall{Match: []string{"version"}, Stdout: "git version 2.43.0\n"})
			raw, calls := f.info(t, f.repo, nil)
			test, after := startTestCalls(t, calls)
			wantFailedTest(t, f, raw, after, c.verb, "error: first forced line fatal: second forced line (git version 2.43.0)")
			if len(test) != c.calls || !slices.Equal(test[c.calls-1].argv, append(shadowTestPrefix(), "version")) {
				t.Errorf("the test made %d calls, want %d with the second version call last", len(test), c.calls)
			}
		})
	}
}

// A server with no settings control answers a git.info with no git in the
// PATH as "no repository" too, and it makes no folder.
func TestGitInfoWithNoGitAndNoRunFolder(t *testing.T) {
	f := newShadowFixture(t)
	// A relative folder that a broken guard makes lands in a folder of the test.
	t.Chdir(t.TempDir())
	f.s.gitShadow = nil
	t.Setenv("PATH", t.TempDir())
	if raw, _ := f.info(t, f.repo, nil); raw != noRepoLine {
		t.Errorf("git.info = %s\nwant %s", raw, noRepoLine)
	}
	if names := daemonFolders(t, f.run); names != nil {
		t.Errorf("git-shadow was made: %v", names)
	}
}

// With GIT_NAMESPACE and GIT_INDEX_FILE in the daemon, no call in the
// folder of the test has either, and call 7 and the excludes read have both.
// The nine such calls of 5fd08069 have neither (cell C8 of round tier0-5).
func TestShadowTestCallsDropNamespaceAndIndexFile(t *testing.T) {
	f := newShadowFixture(t)
	t.Setenv("GIT_NAMESPACE", "x")
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "none", "i"))
	// The calls after the test are not the subject: the request stops at a
	// staged add, with no real git on the two variables.
	stageGit(t, stagedCall{Match: stageAdd, Exit: 128, Stderr: "fatal: forced failure\n"})
	_, calls := f.info(t, f.repo, nil)
	test, _ := startTestCalls(t, calls)
	if len(test) != 11 {
		t.Fatalf("the test made %d calls, want 11", len(test))
	}
	has := func(c envCall) bool {
		_, ns := lookupExact(c.env, "GIT_NAMESPACE")
		_, idx := lookupExact(c.env, "GIT_INDEX_FILE")
		if ns != idx {
			t.Errorf("%v: GIT_NAMESPACE set = %v, GIT_INDEX_FILE set = %v", c.argv, ns, idx)
		}
		return ns
	}
	if !has(calls[0]) {
		t.Error("the excludes read has neither variable, want both")
	}
	for i, c := range test {
		if inFolder := strings.Contains(c.cwd, ".check-"); has(c) == inFolder {
			t.Errorf("test call %d (%v, in the folder of the test: %v) has the variables: %v", i+1, c.argv, inFolder, !inFolder)
		}
	}
	if strings.Contains(test[6].cwd, ".check-") {
		t.Errorf("call 7 runs in %s", test[6].cwd)
	}
}

// With no git in the PATH of the daemon, git.info answers "no
// repository" with no member, runs no test and logs no line about git. The
// folder of the daemon holds the lock file only, and the next request gets
// the same reply. 5fd08069 answers so in 3 of 3 runs (cell C7 of round tier0-5).
func TestGitInfoWithNoGitInThePath(t *testing.T) {
	f := newShadowFixture(t)
	logs := captureLogBuf(t)
	t.Setenv("PATH", t.TempDir())
	for range 2 {
		raw, calls := f.info(t, f.repo, nil)
		if raw != noRepoLine {
			t.Errorf("git.info = %s\nwant %s", raw, noRepoLine)
		}
		if len(calls) != 0 {
			t.Errorf("%d git calls with no git", len(calls))
		}
	}
	if got := entryNames(t, daemonFolder(t, f.run)); !slices.Equal(got, []string{shadowLockName}) {
		t.Errorf("folder of the daemon holds %v, want the lock file only", got)
	}
	if strings.Contains(logs.String(), "[GitHandler]") {
		t.Errorf("log lines of the git handler:\n%s", logs.String())
	}
}
