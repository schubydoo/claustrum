package git

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// setStatusNoGitRule sets the two Windows switches of a git.status with no git on
// PATH for one test.
func setStatusNoGitRule(t *testing.T, on bool) {
	t.Helper()
	oldAnswer, oldReread := statusNoGitAnswer, excludesRereadAfterNoStart
	statusNoGitAnswer, excludesRereadAfterNoStart = on, on
	t.Cleanup(func() { statusNoGitAnswer, excludesRereadAfterNoStart = oldAnswer, oldReread })
}

// With no git on the PATH of the daemon, git.status answers isRepo:false with no
// start test, twice. When git is back, the next request runs the test and keeps no
// failure from before (Linux and macOS).
func TestGitStatusWithNoGitInThePath(t *testing.T) {
	setStatusNoGitRule(t, true)
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

// On Windows the first git.status with no git on PATH answers the cannot-run
// refusal of the start test. When git is back, the next request keeps that
// refusal and makes no git call: no new excludes read and no new test.
func TestGitStatusWithNoGitKeepsTheRefusalOnWindows(t *testing.T) {
	setStatusNoGitRule(t, false)
	f := newShadowFixture(t)
	withGit := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	params := map[string]any{"path": f.repo, "baseRepo": f.repo}
	first, _ := f.call(t, "git.status", params)
	if !strings.Contains(first, `"message":"git cannot run on this host; git not run: exec: `) {
		t.Fatalf("git.status with no git = %s, want the cannot-run refusal", first)
	}
	t.Setenv("PATH", withGit)
	raw, calls := f.call(t, "git.status", params)
	if raw != first || len(calls) != 0 {
		t.Errorf("git.status with git again = %s after %d git calls\nwant %s after none", raw, len(calls), first)
	}
}

// The two switches are off on Windows only.
func TestStatusNoGitRulePerSystem(t *testing.T) {
	want := runtime.GOOS != "windows"
	if statusNoGitAnswer != want || excludesRereadAfterNoStart != want {
		t.Errorf("statusNoGitAnswer %v, excludesRereadAfterNoStart %v on %s, want %v", statusNoGitAnswer, excludesRereadAfterNoStart, runtime.GOOS, want)
	}
}
