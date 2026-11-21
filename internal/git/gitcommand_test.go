package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
// session folders is not kept either (4c111e86, measured on Linux; macOS and
// Windows are claustrum's own, not measured). That reply has no settingsControl
// member, its own git calls name the copy, and the next request with the switch
// tests again. A request without the switch then keeps the state, and a request
// with the switch after it gets "shadow" with no test. Mutation: keep the state of
// a test under the switch (the old gate). The first reply then has "shadow".
func TestStartTestPassUnderOffSwitchIsNotKept(t *testing.T) {
	requireGit(t)
	t.Cleanup(sessiondirs.UseForTest(""))
	f := newShadowFixture(t)
	offInfo := func() (string, []envCall) {
		release := sessiondirs.PauseCheck()
		defer release()
		return f.info(t, f.repo, map[string]any{"sessionFoldersOff": true})
	}
	for i := 1; i <= 2; i++ {
		raw, calls := offInfo()
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
	raw, calls = offInfo()
	if !strings.Contains(raw, `"settingsControl":"shadow"`) || startTestRan(calls) {
		t.Errorf("git.info with the off switch after a kept state = %s (start test %v)\nwant \"shadow\" with no start test", raw, startTestRan(calls))
	}
}
