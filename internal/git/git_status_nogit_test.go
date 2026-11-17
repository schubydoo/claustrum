package git

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setStatusNoGitKeepsTest sets the Windows switch of a git.status with no git on
// PATH for one test.
func setStatusNoGitKeepsTest(t *testing.T, on bool) {
	t.Helper()
	old := statusNoGitKeepsTest
	statusNoGitKeepsTest = on
	t.Cleanup(func() { statusNoGitKeepsTest = old })
}

// With no git on the PATH of the daemon, git.status answers isRepo:false with no
// start test, twice. When git is back, the next request runs the test and keeps no
// failure from before (Linux and macOS).
func TestGitStatusWithNoGitInThePath(t *testing.T) {
	setStatusNoGitKeepsTest(t, false)
	f := newShadowFixture(t)
	withGit := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	params := map[string]any{"path": f.repo, "baseRepo": f.repo}
	for range 2 {
		if raw, _ := f.call(t, "git.status", params); raw != statusNotRepo {
			t.Errorf("git.status with no git = %s\nwant %s", raw, statusNotRepo)
		}
	}
	t.Setenv("PATH", withGit)
	if raw, _ := f.call(t, "git.status", params); raw != statusNotRepo {
		t.Errorf("git.status with git again = %s\nwant %s", raw, statusNotRepo)
	}
}

// The Windows sequence: with no git on PATH git.status answers isRepo:false and
// keeps the failed start test. When git is back, the next request makes one git
// call, the excludes read, and answers the kept refusal. The one after it makes no
// call and answers the same.
func TestGitStatusWithNoGitKeepsTheTestOnWindows(t *testing.T) {
	setStatusNoGitKeepsTest(t, true)
	f := newShadowFixture(t)
	withGit := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	params := map[string]any{"path": f.repo, "baseRepo": f.repo}
	if raw, _ := f.call(t, "git.status", params); raw != statusNotRepo {
		t.Fatalf("git.status with no git = %s\nwant %s", raw, statusNotRepo)
	}
	t.Setenv("PATH", withGit)
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"git cannot run on this host; git not run: exec: \"git\": executable file not found in `
	for i, calls := range []int{1, 0} {
		raw, made := f.call(t, "git.status", params)
		if !strings.HasPrefix(raw, want) || len(made) != calls {
			t.Errorf("git.status %d with git again = %s after %d git calls\nwant %s… after %d", i+1, raw, len(made), want, calls)
		}
	}
	// A baseRepo that does not resolve, as one through a junction, gets the kept
	// refusal too.
	gone := filepath.Join(f.repo, "missing", "w")
	if raw, made := f.call(t, "git.status", map[string]any{"path": gone, "baseRepo": gone}); !strings.HasPrefix(raw, want) || len(made) != 0 {
		t.Errorf("git.status on a path that does not resolve = %s after %d git calls\nwant %s… after none", raw, len(made), want)
	}
}

// The switch is on for Windows only.
func TestStatusNoGitKeepsTestPerSystem(t *testing.T) {
	if want := runtime.GOOS == "windows"; statusNoGitKeepsTest != want {
		t.Errorf("statusNoGitKeepsTest = %v on %s, want %v", statusNoGitKeepsTest, runtime.GOOS, want)
	}
}
