package git

import (
	"os"
	"testing"
)

// With no git on the PATH of the daemon, git.status answers isRepo:false with no
// start test, twice. When git is back, the next request runs the test and keeps no
// failure from before.
func TestGitStatusWithNoGitInThePath(t *testing.T) {
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
