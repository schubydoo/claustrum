package git

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setStatusNoGitRunsTest sets the Windows switch of a git.status with no git on
// PATH for one test.
func setStatusNoGitRunsTest(t *testing.T, on bool) {
	t.Helper()
	old := statusNoGitRunsTest
	statusNoGitRunsTest = on
	t.Cleanup(func() { statusNoGitRunsTest = old })
}

// With no git on the PATH of the daemon, git.status answers isRepo:false with no
// start test. When git is back, the next request runs the test and keeps no
// failure from before (Linux and macOS). With no git, neither the main repository
// nor a linked worktree as baseRepo makes git-shadow (87375cc7, measured on Linux).
// Mutation: make the folder of the daemon (remakeFolder) in the no-git step of
// gitStatus, and git-shadow is there.
func TestGitStatusWithNoGitInThePath(t *testing.T) {
	setStatusNoGitRunsTest(t, false)
	f := newShadowFixture(t)
	linked := filepath.Join(t.TempDir(), "w")
	runGit(t, f.repo, "worktree", "add", "-q", "-b", "w981", linked)
	withGit := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	params := map[string]any{"path": f.repo, "baseRepo": f.repo}
	for _, base := range []string{linked, f.repo, f.repo} {
		if raw, _ := f.call(t, "git.status", map[string]any{"path": base, "baseRepo": base}); raw != statusNotRepo {
			t.Errorf("git.status of %s with no git = %s\nwant %s", base, raw, statusNotRepo)
		}
		if _, err := os.Lstat(filepath.Join(f.run, shadowRootName)); !os.IsNotExist(err) {
			t.Errorf("git-shadow after git.status of %s: %v, want no such folder", base, err)
		}
	}
	t.Setenv("PATH", withGit)
	if raw, _ := f.call(t, "git.status", params); raw != statusNotRepo {
		t.Errorf("git.status with git again = %s\nwant %s", raw, statusNotRepo)
	}
}

// The Windows sequence: with no git on PATH git.status answers isRepo:false
// after a failed start test. That failure is not kept, so when git is back the
// next request runs its own test and the calls of the status (87375cc7, measured
// on Windows). Mutation: drop noGitStarts in gate, and the kept refusal answers.
func TestGitStatusWithNoGitKeepsNoTestOnWindows(t *testing.T) {
	setStatusNoGitRunsTest(t, true)
	f := newShadowFixture(t)
	withGit := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	params := map[string]any{"path": f.repo, "baseRepo": f.repo}
	if raw, _ := f.call(t, "git.status", params); raw != statusNotRepo {
		t.Fatalf("git.status with no git = %s\nwant %s", raw, statusNotRepo)
	}
	t.Setenv("PATH", withGit)
	raw, made := f.call(t, "git.status", params)
	if raw != statusNotRepo || len(made) < 2 {
		t.Errorf("git.status with git again = %s after %d git calls\nwant %s after the test calls", raw, len(made), statusNotRepo)
	}
	// A baseRepo that does not resolve, as one through a junction, gets isRepo:false.
	gone := filepath.Join(f.repo, "missing", "w")
	if raw, made := f.call(t, "git.status", map[string]any{"path": gone, "baseRepo": gone}); raw != statusNotRepo || len(made) != 0 {
		t.Errorf("git.status on a path that does not resolve = %s after %d git calls\nwant %s after none", raw, len(made), statusNotRepo)
	}
}

// The switch is on for Windows only.
func TestStatusNoGitRunsTestPerSystem(t *testing.T) {
	if want := runtime.GOOS == "windows"; statusNoGitRunsTest != want {
		t.Errorf("statusNoGitRunsTest = %v on %s, want %v", statusNoGitRunsTest, runtime.GOOS, want)
	}
}
