package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// shadowFixture is a repository with a linked worktree, a server with a run
// folder, and the git stub with its call log.
type shadowFixture struct {
	refusalFixture
	s   *server
	run string
	log string
}

// newShadowServer returns a server whose settings control has a run folder, as
// the -serve daemon has it.
func newShadowServer(t *testing.T) (*server, string) {
	t.Helper()
	s := newTestServer(t)
	run := resolveTestRoot(t, t.TempDir())
	s.gitShadow = newShadowHost(run)
	t.Cleanup(s.gitShadow.close)
	return s, run
}

func newShadowFixture(t *testing.T) shadowFixture {
	t.Helper()
	f := newRefusalFixture(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	installGitSlowStub(t, realGit)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	clearDaemonConfigEnv(t)
	for _, k := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_OBJECT_DIRECTORY", "LC_ALL", "LANGUAGE"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	resetUserExcludesCache(t)
	log := filepath.Join(t.TempDir(), "env.log")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVLOG", log)
	s, run := newShadowServer(t)
	return shadowFixture{refusalFixture: f, s: s, run: run, log: log}
}

// call dispatches method and returns the reply and the git calls that it made.
func (f shadowFixture) call(t *testing.T, method string, params map[string]any) (string, []envCall) {
	t.Helper()
	if err := os.WriteFile(f.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	raw := dispatchRaw(t, f.s, rpcLine(t, method, params))
	return raw, readEnvCalls(t, f.log)
}

// info is one git.info on path with the extra members.
func (f shadowFixture) info(t *testing.T, path string, extra map[string]any) (string, []envCall) {
	t.Helper()
	params := map[string]any{"path": path}
	for k, v := range extra {
		params[k] = v
	}
	return f.call(t, "git.info", params)
}

// daemonFolders are the names in <run>/git-shadow, or nil when there is no such
// folder.
func daemonFolders(t *testing.T, run string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(run, shadowRootName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// daemonFolder is the one folder of a daemon in <run>/git-shadow.
func daemonFolder(t *testing.T, run string) string {
	t.Helper()
	names := daemonFolders(t, run)
	if len(names) != 1 {
		t.Fatalf("git-shadow holds %v, want one folder", names)
	}
	return filepath.Join(run, shadowRootName, names[0])
}

func entryNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// testCalls are the calls that name a .check- folder in their argv, their
// working directory or their environment.
func testCalls(calls []envCall) []envCall {
	var out []envCall
	for _, c := range calls {
		if strings.Contains(c.cwd+"\x00"+strings.Join(c.argv, "\x00")+"\x00"+strings.Join(c.env, "\x00"), ".check-") {
			out = append(out, c)
		}
	}
	return out
}

func stateLines(log string) int {
	return strings.Count(log, "[GitHandler] this host's git ")
}

func jsonQuoted(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// failedTestText is the text of a failed start test with cause.
func failedTestText(cause string) string {
	return "Claude could not test git on the remote machine (" + cause + "). Check free disk space and quota there, then try again."
}

// useShadowClock replaces the clock of the kept times with one that the test
// moves.
func useShadowClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Now()
	old := shadowClock
	shadowClock = func() time.Time { return now }
	t.Cleanup(func() { shadowClock = old })
	return &now
}

const (
	noRepoLine   = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`
	noRepoShadow = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}}`
	noRepoOff    = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"off-switch"}}`
	noRepoOldGit = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"fallback-git"}}`
)

// TestSettingsControlMember pins the member settingsControl of git.info in each
// reply where 5fd08069 has it, and its absence where that build has none: before
// the start test (s3-linux cell A1), after it (cells A1 and D1), with the member
// shadowDirOff (cells A1 and B1, s3p2a-edges cell X1) and for the values of that
// member that are not the JSON true (cell B3). All on a Linux VM. It also pins
// that no other method has the member, and the one log line of the state.
func TestSettingsControlMember(t *testing.T) {
	f := newShadowFixture(t)
	logs := captureLogBuf(t)
	plain := resolveTestRoot(t, t.TempDir())
	off := map[string]any{"shadowDirOff": true}

	// Before the first test: no member, no folder, also for a folder with no
	// repository. With the switch: off-switch, and still no folder.
	if raw, _ := f.info(t, plain, nil); raw != noRepoLine {
		t.Errorf("no repository, first request = %s\nwant %s", raw, noRepoLine)
	}
	if raw, _ := f.info(t, plain, off); raw != noRepoOff {
		t.Errorf("no repository with the switch = %s\nwant %s", raw, noRepoOff)
	}
	raw, calls := f.info(t, f.repo, off)
	if !strings.HasSuffix(raw, `"defaultBranch":"","settingsControl":"off-switch"}}`) || !strings.Contains(raw, `"isRepo":true`) {
		t.Errorf("repository with the switch as the first request = %s", raw)
	}
	if len(testCalls(calls)) != 0 {
		t.Errorf("a request with the switch ran %d test calls", len(testCalls(calls)))
	}
	// A remove in a folder with no repository reaches the gate with a "no
	// repository" verdict. It starts no test.
	f.call(t, "git.worktree_remove", map[string]any{"baseRepo": plain, "worktreePath": filepath.Join(plain, ".claude", "worktrees", "gone")})
	if names := daemonFolders(t, f.run); names != nil {
		t.Fatalf("requests with the switch or with no repository made git-shadow: %v", names)
	}
	if n := stateLines(logs.String()); n != 0 {
		t.Errorf("%d state lines before a test", n)
	}

	// The first request without the switch on a repository runs the test.
	raw, calls = f.info(t, f.repo, nil)
	const head = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"repo":"repo","branch":"main","root":`
	const tail = `,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}}`
	if !strings.HasPrefix(raw, head) || !strings.HasSuffix(raw, tail) {
		t.Errorf("first git.info = %s\nwant %s<root>%s", raw, head, tail)
	}
	if len(testCalls(calls)) == 0 {
		t.Error("the first request without the switch ran no test call")
	}
	if strings.Count(logs.String(), shadowStateLine) != 1 || stateLines(logs.String()) != 1 {
		t.Errorf("log after the first test:\n%s\nwant the shadow line one time", logs.String())
	}

	// After the test the "no repository" reply has the member too.
	if raw, _ := f.info(t, plain, nil); raw != noRepoShadow {
		t.Errorf("no repository after the test = %s\nwant %s", raw, noRepoShadow)
	}
	if raw, _ := f.info(t, plain, off); raw != noRepoOff {
		t.Errorf("no repository with the switch after the test = %s\nwant %s", raw, noRepoOff)
	}
	// Only the JSON true is the switch.
	for _, v := range []any{"true", 1, nil} {
		raw, calls := f.info(t, f.repo, map[string]any{"shadowDirOff": v})
		if !strings.HasSuffix(raw, tail) {
			t.Errorf("shadowDirOff %#v = %s, want the state shadow", v, raw)
		}
		if len(testCalls(calls)) != 0 {
			t.Errorf("shadowDirOff %#v: a second test ran", v)
		}
	}
	if n := stateLines(logs.String()); n != 1 {
		t.Errorf("%d state lines after more requests, want 1", n)
	}
	// No other method has the member.
	for _, m := range f.fiveRequests(nil)[1:] {
		if raw := dispatchRaw(t, f.s, rpcLine(t, m.method, m.params)); strings.Contains(raw, "settingsControl") {
			t.Errorf("%s = %s, want no settingsControl", m.method, raw)
		}
	}
}

// A server with no run folder has no settings control: no member, with and
// without the switch, and no test call. That is the server of the unit tests.
// The -serve daemon always has the folder of its socket.
func TestSettingsControlNeedsARunFolder(t *testing.T) {
	f := newShadowFixture(t)
	// A relative folder that a broken guard makes lands in a folder of the test.
	t.Chdir(t.TempDir())
	for _, host := range []*shadowHost{nil, newShadowHost("")} {
		f.s = newTestServer(t)
		f.s.gitShadow = host
		for _, extra := range []map[string]any{nil, {"shadowDirOff": true}} {
			raw, calls := f.info(t, f.repo, extra)
			if strings.Contains(raw, "settingsControl") || !strings.Contains(raw, `"isRepo":true`) {
				t.Errorf("git.info %v = %s, want no settingsControl", extra, raw)
			}
			if len(testCalls(calls)) != 0 {
				t.Errorf("git.info %v ran %d test calls", extra, len(testCalls(calls)))
			}
		}
		host.close()
	}
}

// TestShadowFolderName pins the name of the folder of a daemon: the host name
// with each "-" as "_", the pid and 16 hex digits. On 5fd08069 a host name of
// the form aa-bb gives aa_bb-17044-82053e1e2c228715 (s3-linux cell C6, Linux
// VM), and a "." stays (macOS VM).
func TestShadowFolderName(t *testing.T) {
	f := newShadowFixture(t)
	old := shadowHostname
	shadowHostname = func() (string, error) { return "aa-bb-cc.local", nil }
	t.Cleanup(func() { shadowHostname = old })
	f.info(t, f.repo, nil)
	name := filepath.Base(daemonFolder(t, f.run))
	want := regexp.MustCompile(`^aa_bb_cc\.local-` + strconv.Itoa(os.Getpid()) + `-[0-9a-f]{16}$`)
	if !want.MatchString(name) {
		t.Errorf("folder of the daemon = %q, want %s", name, want)
	}
}

// TestShadowFolderLife pins the folder of one daemon over its life. It is made
// at the first request without the switch and holds only the lock file after
// the test. A folder that was removed is made again with another hex part and
// with no second test (s3-linux cell B6, Linux VM). Two daemons in one run
// folder each have their own, and the end of one leaves the other
// (s3p2b-edges-linux2 cell 44, Linux VM). At the end the folder goes and the
// empty git-shadow stays (s3p2-linux section 1).
func TestShadowFolderLife(t *testing.T) {
	f := newShadowFixture(t)
	f.info(t, f.repo, nil)
	first := daemonFolder(t, f.run)
	// The test folder is gone. The folder of the call about the repository
	// and the folder of the repository are there (s3-linux cell A3, Linux VM).
	if got, want := entryNames(t, first), []string{shadowProbeName, shadowRepoName(filepath.Join(f.repo, ".git")), shadowLockName}; !slices.Equal(got, want) {
		t.Fatalf("folder of the daemon after the test holds %v, want %v", got, want)
	}
	if fi, err := os.Stat(filepath.Join(first, shadowLockName)); err != nil || fi.Size() != 0 {
		t.Fatalf("lock file: %v, %v, want 0 bytes", fi, err)
	}

	// The folder goes while the daemon runs. The handle is closed first, so
	// that the remove works on Windows too.
	f.s.gitShadow.mu.Lock()
	f.s.gitShadow.dropLock()
	f.s.gitShadow.mu.Unlock()
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Errorf("git.info after the folder went = %s", raw)
	}
	if n := len(testCalls(calls)); n != 0 {
		t.Errorf("the request after the folder went ran %d test calls", n)
	}
	second := daemonFolder(t, f.run)
	if second == first {
		t.Errorf("the new folder has the old name %s", filepath.Base(first))
	}
	if got, want := entryNames(t, second), []string{shadowRepoName(filepath.Join(f.repo, ".git")), shadowLockName}; !slices.Equal(got, want) {
		t.Errorf("the new folder holds %v, want %v", got, want)
	}

	// A second daemon in the same run folder.
	other := newTestServer(t)
	other.gitShadow = newShadowHost(f.run)
	if raw := dispatchRaw(t, other, rpcLine(t, "git.info", map[string]any{"path": f.repo})); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info of the second daemon = %s", raw)
	}
	if names := daemonFolders(t, f.run); len(names) != 2 {
		t.Fatalf("git-shadow with two daemons holds %v", names)
	}
	other.gitShadow.close()
	if got := daemonFolder(t, f.run); got != second {
		t.Errorf("after the end of the second daemon git-shadow holds %s, want %s", got, second)
	}

	f.s.gitShadow.close()
	if names := daemonFolders(t, f.run); names == nil || len(names) != 0 {
		t.Errorf("git-shadow after the end holds %v, want an empty folder", names)
	}
	// A request after the end makes no folder again. It is refused, because its
	// git calls have no copy to name (TestShadowRepoFailureInTheShutdown).
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, "the daemon is in its shutdown") {
		t.Errorf("git.info after the end = %s", raw)
	}
	if names := daemonFolders(t, f.run); len(names) != 0 {
		t.Errorf("a request after the end made %v", names)
	}
}

// failBothAdds makes the stub fail both `worktree add` calls of the test with
// the line of the wrapper of s3p2b-edges-linux2 cell 26.
func failBothAdds(t *testing.T) {
	t.Helper()
	slowGit(t, "worktree,add", "fail", 0, "fatal: forced failure by the test wrapper\n", "")
}

// TestShadowTestFailureShapes pins the answer of each method to a failed start
// test, as the first request of its own daemon. 5fd08069 gives the error object
// on git.info, git.status and git.list_branches, and the result object on
// git.worktree_create and git.worktree_remove (s3p2b-edges-linux2 cell 30, Linux
// VM). The cause is the one of cell 26: both `worktree add` calls of the test
// fail. The failure is kept: the same request again makes no git call. A request
// with the switch works then (cell 32). Nothing is made or removed, the folder
// of the daemon holds only the lock file, and no state line is logged.
func TestShadowTestFailureShapes(t *testing.T) {
	for i, method := range []string{"git.info", "git.status", "git.list_branches", "git.worktree_create", "git.worktree_remove"} {
		t.Run(method, func(t *testing.T) {
			f := newShadowFixture(t)
			logs := captureLogBuf(t)
			failBothAdds(t)
			m := f.fiveRequests(nil)[i]
			raw, calls := f.call(t, m.method, m.params)
			dir := daemonFolder(t, f.run)
			text := failedTestText("git worktree failed under " + strconv.Quote(dir) + ": fatal: forced failure by the test wrapper")
			want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` + jsonQuoted(t, text) + `}}`
			switch method {
			case "git.worktree_create":
				want = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonQuoted(t, text) + `,"errorCode":"worktree_add_failed"}}`
			case "git.worktree_remove":
				want = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
					jsonQuoted(t, "failed to remove worktree: could not check whether "+f.wt+" is locked: "+text) + `}}`
			}
			if raw != want {
				t.Errorf("%s = %s\nwant %s", method, raw, want)
			}
			// The excludes read, the ten calls of the test and the second add.
			if len(calls) != 12 || !calls[0].excludesRead() || len(testCalls(calls)) != 10 {
				t.Errorf("%s made %d calls, %d of the test, want 12 and 10", method, len(calls), len(testCalls(calls)))
			}
			again, calls := f.call(t, m.method, m.params)
			if again != want || len(calls) != 0 {
				t.Errorf("%s again = %s with %d calls\nwant the kept text with no call", method, again, len(calls))
			}
			if raw, _ := f.info(t, f.repo, map[string]any{"shadowDirOff": true}); !strings.Contains(raw, `"isRepo":true`) || !strings.Contains(raw, `"settingsControl":"off-switch"`) {
				t.Errorf("git.info with the switch while the failure is kept = %s", raw)
			}
			if _, err := os.Stat(f.wt); err != nil {
				t.Errorf("the worktree of the fixture: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.repo, ".claude", "worktrees", "w1")); !os.IsNotExist(err) {
				t.Errorf("a worktree was made: %v", err)
			}
			if got := entryNames(t, dir); !slices.Equal(got, []string{shadowLockName}) {
				t.Errorf("folder of the daemon after the failed test holds %v", got)
			}
			if n := stateLines(logs.String()); n != 0 {
				t.Errorf("%d state lines after a failed test", n)
			}
		})
	}
}

// TestShadowTestFailureKeep pins both sides of the 10 s for which a failure is
// kept, with the clock of the test. On 5fd08069 a request 9.90 s after the
// reply made no call and one 10.00 s after it ran the test again. A repeat test
// that fails starts a new 10 s. The failure holds after its cause is gone
// (s3p2b-edges-linux2 cells 26 and 27 and s3p2-linux2 cell C1b, Linux VM).
func TestShadowTestFailureKeep(t *testing.T) {
	f := newShadowFixture(t)
	now := useShadowClock(t)
	failBothAdds(t)
	first, _ := f.info(t, f.repo, nil)
	if !strings.Contains(first, "Claude could not test git on the remote machine") {
		t.Fatalf("first git.info = %s", first)
	}
	*now = now.Add(shadowFailureKeep - time.Nanosecond)
	if raw, calls := f.info(t, f.repo, nil); raw != first || len(calls) != 0 {
		t.Errorf("just inside the keep: %s with %d calls, want the kept text and no call", raw, len(calls))
	}
	*now = now.Add(time.Nanosecond)
	raw, calls := f.info(t, f.repo, nil)
	if raw != first || len(calls) != 11 || len(testCalls(calls)) != 10 {
		t.Errorf("at the end of the keep: %s with %d calls, %d of the test\nwant a new test: 11 and 10", raw, len(calls), len(testCalls(calls)))
	}
	// The repeat started a new keep, and the cause is gone now.
	slowGit(t, "", "", 0, "", "")
	*now = now.Add(shadowFailureKeep - time.Nanosecond)
	if raw, calls := f.info(t, f.repo, nil); raw != first || len(calls) != 0 {
		t.Errorf("inside the keep of the repeat, the cause gone: %s with %d calls", raw, len(calls))
	}
	*now = now.Add(time.Nanosecond)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Errorf("after the keep, the cause gone = %s", raw)
	}
}

// TestShadowFallbackAfterFailedAdd pins the state fallback-git of a git whose
// first test `worktree add` fails and whose second one works (s3p2b-edges-linux2
// cell 29, Linux VM), its log line, and the repeat of the test after 10 minutes
// (cell 28 and s3p2-oldgit cell O5: no test at 599.8 s, a test at 605.5 s). A
// repeat with the same result logs no line. A repeat that passes changes the
// state to shadow and logs the other line (cell O5). In the state fallback-git
// the daemon reads no config of the repository: format version 2 gets the "no
// repository" answer with the member (cell O4).
func TestShadowFallbackAfterFailedAdd(t *testing.T) {
	f := newShadowFixture(t)
	logs := captureLogBuf(t)
	now := useShadowClock(t)
	failBothAdds(t)
	t.Setenv("CLAUSTRUM_GITSTUB_ARGEND", string(os.PathSeparator)+"wt")

	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"isRepo":true`) || !strings.HasSuffix(raw, `"settingsControl":"fallback-git"}}`) {
		t.Fatalf("git.info with a failed first add = %s", raw)
	}
	var adds []string
	for _, c := range testCalls(calls) {
		if slices.Contains(c.argv, "worktree") {
			adds = append(adds, filepath.Base(c.argv[len(c.argv)-2]))
			if _, shadow := lookupExact(c.env, "GIT_COMMON_DIR"); shadow != (len(adds) == 1) {
				t.Errorf("add %d: GIT_COMMON_DIR set = %v", len(adds), shadow)
			}
		}
	}
	if !slices.Equal(adds, []string{"wt", "wt2"}) {
		t.Errorf("the adds of the test = %v, want wt and wt2", adds)
	}
	if strings.Count(logs.String(), fallbackStateLine) != 1 || stateLines(logs.String()) != 1 {
		t.Errorf("log:\n%s\nwant the fallback-git line one time", logs.String())
	}
	if got := entryNames(t, daemonFolder(t, f.run)); !slices.Equal(got, []string{shadowLockName}) {
		t.Errorf("folder of the daemon in the state fallback-git holds %v", got)
	}

	// No read of the config, and no refusal of it.
	setRepoConfig(t, f.gitDir, "[core]\n\trepositoryformatversion = 2\n"+baseRepoConfig)
	raw, calls = f.info(t, f.repo, nil)
	if raw != noRepoOldGit {
		t.Errorf("format version 2 in the state fallback-git = %s\nwant %s", raw, noRepoOldGit)
	}
	for _, c := range calls {
		if c.configRead() {
			t.Errorf("the state fallback-git read a config file: %v", c.argv)
		}
	}
	setRepoConfig(t, f.gitDir, "[core]\n\trepositoryformatversion = 0\n"+baseRepoConfig)

	// The repeat: not at 10 minutes, and at the next moment.
	*now = now.Add(shadowRetestAfter)
	if _, calls := f.info(t, f.repo, nil); len(testCalls(calls)) != 0 {
		t.Errorf("a test ran at %v after the last one", shadowRetestAfter)
	}
	*now = now.Add(time.Nanosecond)
	raw, calls = f.info(t, f.repo, nil)
	if len(testCalls(calls)) == 0 || !strings.HasSuffix(raw, `"settingsControl":"fallback-git"}}`) {
		t.Errorf("past %v: %d test calls, reply %s", shadowRetestAfter, len(testCalls(calls)), raw)
	}
	if n := stateLines(logs.String()); n != 1 {
		t.Errorf("%d state lines after a repeat with the same result, want 1", n)
	}
	// The repeat counts from its own end.
	*now = now.Add(shadowRetestAfter)
	if _, calls := f.info(t, f.repo, nil); len(testCalls(calls)) != 0 {
		t.Error("a test ran inside the time of the repeat")
	}
	// The git works now.
	slowGit(t, "", "", 0, "", "")
	*now = now.Add(time.Nanosecond)
	if raw, _ := f.info(t, f.repo, nil); !strings.HasSuffix(raw, `"settingsControl":"shadow"}}`) {
		t.Errorf("after a repeat that passes = %s", raw)
	}
	if strings.Count(logs.String(), shadowStateLine) != 1 || stateLines(logs.String()) != 2 {
		t.Errorf("log after the change of the state:\n%s", logs.String())
	}
	// In the state shadow no test runs again.
	*now = now.Add(3 * shadowRetestAfter)
	if _, calls := f.info(t, f.repo, nil); len(testCalls(calls)) != 0 {
		t.Error("a second test ran in the state shadow")
	}
}

// TestShadowFallbackAfterQuietRevParse pins the other way into fallback-git:
// `rev-parse --verify -q HEAD^{commit}` exits 1 with no output with the two
// variables of the copy, and exits 0 without them. No `worktree add` runs then.
// git 2.11.4 and 2.12.5 answer so on a Linux VM (s3p2-oldgit section 4), and the
// wrapper rule of s3p2b-edges-linux2 cell 28 does.
func TestShadowFallbackAfterQuietRevParse(t *testing.T) {
	f := newShadowFixture(t)
	slowGit(t, "rev-parse,--verify", "fail", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_COMMON_DIR="+string(os.PathSeparator)+"shadow")
	raw, calls := f.info(t, f.repo, nil)
	if !strings.HasSuffix(raw, `"settingsControl":"fallback-git"}}`) {
		t.Fatalf("git.info = %s, want the state fallback-git", raw)
	}
	var verifies int
	for _, c := range testCalls(calls) {
		if slices.Contains(c.argv, "worktree") {
			t.Errorf("a worktree call ran: %v", c.argv)
		}
		if slices.Contains(c.argv, "--verify") {
			verifies++
			if _, shadow := lookupExact(c.env, "GIT_COMMON_DIR"); shadow != (verifies == 1) {
				t.Errorf("rev-parse %d: GIT_COMMON_DIR set = %v", verifies, shadow)
			}
			if _, ok := lookupExact(c.env, "GIT_OBJECT_DIRECTORY"); ok != (verifies == 1) {
				t.Errorf("rev-parse %d: GIT_OBJECT_DIRECTORY set = %v", verifies, ok)
			}
		}
	}
	// Calls 1 to 6, 8 and 9 name the folder of the test, and the second
	// rev-parse does.
	if verifies != 2 || len(testCalls(calls)) != 9 {
		t.Errorf("%d rev-parse calls and %d test calls, want 2 and 9", verifies, len(testCalls(calls)))
	}
}

// TestShadowDeleteRefusesHome pins the home guard of the deletes below
// git-shadow. The guard is defense in depth: every name there is made by the
// daemon. The home folder of the test is a folder of the test itself, so a run
// without the guard deletes nothing else.
func TestShadowDeleteRefusesHome(t *testing.T) {
	run := resolveTestRoot(t, t.TempDir())
	home := filepath.Join(run, shadowRootName, "name")
	writeFile(t, filepath.Join(home, "inside", "keep.txt"), "x", 0o644)
	t.Setenv("HOME", filepath.Join(home, "inside"))
	t.Setenv("USERPROFILE", filepath.Join(home, "inside"))
	h := newShadowHost(run)
	if err := h.removeEntry("name"); err == nil {
		t.Error("removeEntry deleted a folder that holds the home folder")
	}
	if _, err := os.Stat(filepath.Join(home, "inside", "keep.txt")); err != nil {
		t.Errorf("the file in the home folder: %v", err)
	}
	// The control: with the home folder elsewhere the same call deletes.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if err := h.removeEntry("name"); err != nil {
		t.Errorf("removeEntry with the home folder elsewhere: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("the folder after the control delete: %v, want it gone", err)
	}
}

// TestShadowTestCopyTexts pins the two texts of HEAD and of the config in the
// folder `shadow` of the start test, and the step of each. 5fd08069 writes
// `ref: refs/heads/.invalid` and a config of 61 bytes first. Later it writes the
// HEAD of `real` and the text of the copied settings with the helper.check
// block. The rows are section 1.3 of val-s3p2b2-1-linux, D1 and D2 of
// val-s3p2b2-1-macos and D3 of val-s3p2b2-1-windows. The first texts are there
// at call 6. claustrum
// has the second texts from call 8 on: the records fix only that they come
// before the end of call 9. The folder `worktrees` of `real` is there before
// call 10 on every system (the same rows).
func TestShadowTestCopyTexts(t *testing.T) {
	f := newShadowFixture(t)
	copyLog := filepath.Join(t.TempDir(), "copy.log")
	t.Setenv("CLAUSTRUM_GITSTUB_COPYLOG", copyLog)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	b, err := os.ReadFile(copyLog)
	if err != nil {
		t.Fatal(err)
	}
	type seen struct{ argv, head, config, worktrees string }
	var calls []seen
	for _, rec := range strings.Split(string(b), "\x1d\n") {
		if p := strings.Split(rec, "\x1e"); len(p) == 4 && strings.Contains(p[0], "commit.gpgsign=false") {
			calls = append(calls, seen{strings.ReplaceAll(p[0], "\x1f", " "), p[1], p[2], p[3]})
		}
	}
	if len(calls) != 4 {
		t.Fatalf("%d test calls with the folder of the copy, want calls 6, 8, 9 and 10", len(calls))
	}
	const helper = "[helper]\n\tcheck = shadow\n"
	first := seen{head: "ref: refs/heads/.invalid\n", config: "[core]\n\trepositoryformatversion = 0\n" + helper}
	if len(first.config) != 61 || len(first.head) != 25 {
		t.Fatalf("the first texts have %d and %d bytes, want 61 and 25", len(first.config), len(first.head))
	}
	if calls[0].head != first.head || calls[0].config != first.config {
		t.Errorf("call 6 sees HEAD %q and config %q\nwant %q and %q", calls[0].head, calls[0].config, first.head, first.config)
	}
	for i, c := range calls[1:] {
		if !strings.HasPrefix(c.head, "ref: refs/heads/") || c.head == first.head {
			t.Errorf("call %d sees HEAD %q, want the HEAD of the repository of the test", i+8, c.head)
		}
		if !strings.HasPrefix(c.config, "[core]\n\trepositoryformatversion = \"0\"\n[core]\n\tfilemode = \"") ||
			!strings.HasSuffix(c.config, "[lfs]\n\trepositoryformatversion = 0\n"+helper) {
			t.Errorf("call %d sees config %q\nwant the copied settings, the lfs block and the helper.check block", i+8, c.config)
		}
		if runtime.GOOS == "linux" && len(c.config) != 181 {
			t.Errorf("call %d sees a config of %d bytes, want 181 on Linux", i+8, len(c.config))
		}
	}
	if last := calls[3]; !strings.Contains(last.argv, "worktree add") || last.worktrees != "worktrees folder" {
		t.Errorf("call 10 = %q with %q, want the add with the worktrees folder of the repository in place", last.argv, last.worktrees)
	}
}
