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

// A git.info with "sessionFoldersOff":true alone answers with no settingsControl
// member where sessionOffDropsControl holds (Windows), and keeps it elsewhere.
// With "shadowDirOff":true too the member is "off-switch".
func TestSessionOffAloneDropsTheControlMember(t *testing.T) {
	requireGit(t)
	t.Cleanup(sessiondirs.UseForTest(""))
	f := newSeededShadowFixture(t)
	old := sessionOffDropsControl
	t.Cleanup(func() { sessionOffDropsControl = old })
	for _, drops := range []bool{false, true} {
		sessionOffDropsControl = drops
		raw, _ := f.info(t, f.repo, map[string]any{"sessionFoldersOff": true})
		if has := strings.Contains(raw, `"settingsControl"`); has == drops || !strings.Contains(raw, `"isRepo":true`) {
			t.Errorf("drops=%v: git.info = %s", drops, raw)
		}
		raw, _ = f.info(t, f.repo, map[string]any{"sessionFoldersOff": true, "shadowDirOff": true})
		if !strings.Contains(raw, `"settingsControl":"off-switch"`) {
			t.Errorf("drops=%v: git.info with both = %s", drops, raw)
		}
		raw, _ = f.info(t, f.repo, nil)
		if !strings.Contains(raw, `"settingsControl":"shadow"`) {
			t.Errorf("drops=%v: plain git.info = %s", drops, raw)
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

// The settingsControl member goes under "sessionFoldersOff":true alone on macOS and
// Windows, and stays on Linux.
func TestSessionOffDropsControlPerSystem(t *testing.T) {
	want := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if sessionOffDropsControl != want {
		t.Errorf("sessionOffDropsControl = %v on %s, want %v", sessionOffDropsControl, runtime.GOOS, want)
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
