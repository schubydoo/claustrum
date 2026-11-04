package git

import (
	"os"
	"path/filepath"
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
	sessiondirs.Remember(s)
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
