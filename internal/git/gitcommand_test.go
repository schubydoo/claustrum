package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// The environment of a git child of the git methods, and of a call of the start
// test, has no PATH entry in a session folder.
func TestGitChildEnvCutsSessionFolders(t *testing.T) {
	t.Cleanup(sessiondirs.UseForTest(""))
	s := t.TempDir()
	other := t.TempDir()
	_ = sessiondirs.Remember(s)
	t.Setenv("PATH", filepath.Join(s, "bin")+string(os.PathListSeparator)+other)
	for name, env := range map[string][]string{"gitChildEnv": gitChildEnv(), "shadowDaemonEnv": shadowDaemonEnv()} {
		if v, ok := envValue(env, "PATH"); !ok || v != other {
			t.Errorf("%s PATH = %q %v, want %q", name, v, ok, other)
		}
	}
}

// After a start test gave a state, a git.info with "sessionFoldersOff":true alone
// answers a repository with the settingsControl member "shadow", as 4c111e86 does
// (measured on Linux, macOS and Windows). With "shadowDirOff":true too the member
// is "off-switch".
func TestSessionOffAloneKeepsTheControlMember(t *testing.T) {
	requireGit(t)
	t.Cleanup(sessiondirs.UseForTest(""))
	f := newSeededShadowFixture(t)
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"sessionFoldersOff": true}, `"settingsControl":"shadow"`},
		{map[string]any{"sessionFoldersOff": true, "shadowDirOff": true}, `"settingsControl":"off-switch"`},
		{nil, `"settingsControl":"shadow"`},
	} {
		raw, _ := f.info(t, f.repo, c.params)
		if !strings.Contains(raw, c.want) || !strings.Contains(raw, `"isRepo":true`) {
			t.Errorf("git.info %v = %s\nwant %s", c.params, raw, c.want)
		}
	}
}

// The eight editor names of the daemon environment reach no git child of the git
// methods. Another name stays.
func TestGitChildEnvDropsTheEditorNames(t *testing.T) {
	for _, n := range sessiondirs.EditorNames {
		t.Setenv(n, "x")
	}
	t.Setenv("FOOCTL", "x")
	for name, env := range map[string][]string{"gitChildEnv": gitChildEnv(), "shadowDaemonEnv": shadowDaemonEnv()} {
		for _, n := range sessiondirs.EditorNames {
			if _, ok := envValue(env, n); ok {
				t.Errorf("%s has %s", name, n)
			}
		}
		if v, ok := envValue(env, "FOOCTL"); !ok || v != "x" {
			t.Errorf("%s FOOCTL = %q %v", name, v, ok)
		}
	}
}

// With every git on PATH in a session folder, the start test answers the
// cannot-run text with the refusal, twice, for each of the read methods.
func TestStartTestWithNoOtherGit(t *testing.T) {
	requireGit(t)
	t.Cleanup(sessiondirs.UseForTest(""))
	f := newShadowFixture(t)
	stub, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Dir(stub)
	_ = sessiondirs.Remember(bin)
	t.Setenv("PATH", bin)
	want := `"message":"git cannot run on this host; git not run: ` + sessiondirs.NoOtherGitError{}.Error() + `"`
	if runtime.GOOS == "windows" {
		want = strings.ReplaceAll(want, `\`, `\\`)
	}
	for _, method := range []string{"git.info", "git.list_branches", "git.info"} {
		raw, _ := f.call(t, method, map[string]any{"path": f.repo})
		if !strings.Contains(raw, want) {
			t.Errorf("%s = %s\nwant %s", method, raw, want)
		}
	}
}

// The refusal of a remove of a gone worktree puts a cannot-run reason and a trust
// refusal after a colon, and keeps the parentheses for another reason.
func TestGoneLockCheckText(t *testing.T) {
	got := goneLockCheckText("/w", gitCannotRunPrefix+"exit status 1")
	if want := "failed to remove worktree: could not check whether /w is locked: git cannot run on this host; git not run: exit status 1"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	got = goneLockCheckText("/w", gitDirTrustPrefix+"x")
	if want := "failed to remove worktree: could not check whether /w is locked: the repository's git directory could not be trusted; git not run: commondir not as git writes it: x"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	got = goneLockCheckText("/w", "boom")
	if want := "failed to remove worktree: could not check whether /w is locked (boom); retry"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

// A git that fails `init` and `git version` in the start test, with no session
// folder: git.info answers the cannot-run text of the version call.
func TestStartTestWithAFailingGit(t *testing.T) {
	requireGit(t)
	t.Cleanup(sessiondirs.UseForTest(""))
	f := newShadowFixture(t)
	slowGit(t, "version", "fail", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "1")
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "init")
	t.Setenv("CLAUSTRUM_GITSTUB_MODE2", "fail")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "1")
	raw, _ := f.call(t, "git.info", map[string]any{"path": f.repo})
	if want := `"error":{"code":-32603,"message":"git cannot run on this host; git not run: exit status 1"}`; !strings.Contains(raw, want) {
		t.Errorf("git.info = %s\nwant %s", raw, want)
	}
}

// A start test that failed while a request with the off switch held the check of
// session folders is not kept: the next request without it tests again, and its
// good test answers the repository.
func TestStartTestFailureUnderOffSwitchIsNotKept(t *testing.T) {
	requireGit(t)
	t.Cleanup(sessiondirs.UseForTest(""))
	f := newShadowFixture(t)
	slowGit(t, "version", "fail", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "1")
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "init")
	t.Setenv("CLAUSTRUM_GITSTUB_MODE2", "fail")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "1")
	release := sessiondirs.PauseCheck()
	raw, _ := f.call(t, "git.info", map[string]any{"path": f.repo, "sessionFoldersOff": true})
	release()
	if want := `"message":"git cannot run on this host; git not run: exit status 1"`; !strings.Contains(raw, want) {
		t.Fatalf("git.info with the off switch = %s\nwant %s", raw, want)
	}
	slowGit(t, "", "", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "")
	raw, _ = f.call(t, "git.info", map[string]any{"path": f.repo})
	if want := `"isRepo":true`; !strings.Contains(raw, want) {
		t.Errorf("git.info after the off switch = %s\nwant %s", raw, want)
	}
}

// A start test that passed while a request with the off switch held the check of
// session folders is not kept either (4c111e86, measured on Linux). On macOS and
// Windows the member of the first such request is measured too. The retest and
// the other orders are claustrum's own there. That reply has no settingsControl
// member, its own git calls name the copy, and the next request with the switch
// tests again. A request without the switch then keeps the state, and a request
// with the switch after it gets "shadow" with no test. Mutation: keep the state of
// a test under the switch (the old gate). The first reply then has "shadow".
func TestStartTestPassUnderOffSwitchIsNotKept(t *testing.T) {
	requireGit(t)
	t.Cleanup(sessiondirs.UseForTest(""))
	f := newShadowFixture(t)
	for i := 1; i <= 2; i++ {
		raw, calls := offSwitchInfo(t, f)
		if !strings.Contains(raw, `"isRepo":true`) || strings.Contains(raw, "settingsControl") {
			t.Errorf("git.info %d with the off switch = %s\nwant a repository with no settingsControl", i, raw)
		}
		if !startTestRan(calls) {
			t.Errorf("git.info %d with the off switch ran no start test", i)
		}
		if len(calls) == 0 {
			t.Fatalf("git.info %d with the off switch made no git call", i)
		}
		if v, _ := envValue(calls[len(calls)-1].env, "GIT_COMMON_DIR"); !strings.Contains(v, shadowRootName) {
			t.Errorf("git.info %d with the off switch: last call GIT_COMMON_DIR = %q, want the copy under %s", i, v, shadowRootName)
		}
	}
	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"settingsControl":"shadow"`) || !startTestRan(calls) {
		t.Errorf("git.info without the switch = %s (start test %v)\nwant \"shadow\" after a start test", raw, startTestRan(calls))
	}
	raw, calls = offSwitchInfo(t, f)
	if !strings.Contains(raw, `"settingsControl":"shadow"`) || startTestRan(calls) {
		t.Errorf("git.info with the off switch after a kept state = %s (start test %v)\nwant \"shadow\" with no start test", raw, startTestRan(calls))
	}
}

// offSwitchInfo is one git.info on the repository of f with "sessionFoldersOff":true,
// while the check of session folders is held off, as the daemon holds it for such a
// request.
func offSwitchInfo(t *testing.T, f shadowFixture) (string, []envCall) {
	t.Helper()
	release := sessiondirs.PauseCheck()
	defer release()
	return f.info(t, f.repo, map[string]any{"sessionFoldersOff": true})
}

// The state line of a passed start test is logged at each test that is not kept,
// once more when a request without the switch keeps the state, and not again after
// that (4c111e86 logs one at each test until a state is kept, measured on Linux;
// its count per request is the known gap). Mutation: log the line only at a change
// of the state. The second request with the switch then logs none.
func TestStartTestStateLineUnderOffSwitch(t *testing.T) {
	requireGit(t)
	t.Cleanup(sessiondirs.UseForTest(""))
	f := newShadowFixture(t)
	buf := captureLogBuf(t)
	lines := func() int { return strings.Count(buf.String(), shadowStateLine) }
	for i := 1; i <= 2; i++ {
		offSwitchInfo(t, f)
		if got := lines(); got != i {
			t.Errorf("after git.info %d with the off switch: %d state lines, want %d", i, got, i)
		}
	}
	f.info(t, f.repo, nil)
	if got := lines(); got != 3 {
		t.Errorf("after the git.info that keeps the state: %d state lines, want 3", got)
	}
	offSwitchInfo(t, f)
	f.info(t, f.repo, nil)
	if got := lines(); got != 3 {
		t.Errorf("after two requests with a kept state: %d state lines, want 3, log:\n%s", got, buf.String())
	}
}

// A kept fallback-git that is due for its retest stays as it is after a test under
// the switch that passes. The request uses it, and the reply answers
// "fallback-git" (claustrum's own, not measured). Mutation: let a test under the
// switch replace any state (drop the test of h.state == "" || h.unkept). The state
// then becomes a "shadow" that is not kept, and the reply has no member.
func TestStartTestUnderOffSwitchKeepsAKeptState(t *testing.T) {
	requireGit(t)
	t.Cleanup(sessiondirs.UseForTest(""))
	f := newShadowFixture(t)
	now := time.Now()
	oldClock := shadowClock
	shadowClock = func() time.Time { return now }
	t.Cleanup(func() { shadowClock = oldClock })
	h := f.d.Shadow
	h.mu.Lock()
	h.state, h.tested = controlFallback, now.Add(-shadowRetestAfter-time.Minute)
	h.mu.Unlock()

	raw, calls := offSwitchInfo(t, f)
	if !startTestRan(calls) {
		t.Fatalf("git.info with the off switch and a due retest ran no start test: %s", raw)
	}
	if !strings.Contains(raw, `"settingsControl":"fallback-git"`) {
		t.Errorf("git.info with the off switch = %s\nwant the kept \"fallback-git\"", raw)
	}
	h.mu.Lock()
	state, unkept := h.state, h.unkept
	h.mu.Unlock()
	if state != controlFallback || unkept {
		t.Errorf("state after the test = %q (not kept %v), want the kept %q", state, unkept, controlFallback)
	}
}
