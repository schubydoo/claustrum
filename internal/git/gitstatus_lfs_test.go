package git

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestStatusCallNamesTheLfsFolderOfTheTemporaryGitFolder: in the state shadow
// the `status` call of git.status carries `-c lfs.storage=<.gitdir-<n>>/lfs`,
// (b3081b51, measured on Linux, macOS and Windows). No other call carries it
// (measured on Linux and macOS). With "shadowDirOff":true no call carries it (claustrum's own, not
// measured).
func TestStatusCallNamesTheLfsFolderOfTheTemporaryGitFolder(t *testing.T) {
	f := newSeededShadowFixture(t)
	lfsCalls := func(calls []envCall) (status, other int) {
		for _, c := range calls {
			i := slices.IndexFunc(c.argv, func(a string) bool { return strings.HasPrefix(a, "lfs.storage=") })
			if i < 0 {
				continue
			}
			gitDir := ""
			for _, a := range c.argv {
				if v, ok := strings.CutPrefix(a, "--git-dir="); ok {
					gitDir = v
				}
			}
			if c.argv[i-1] == "-c" && slices.Contains(c.argv, "status") && c.argv[i] == "lfs.storage="+filepath.Join(gitDir, "lfs") &&
				strings.HasPrefix(filepath.Base(gitDir), shadowStatusDirPrefix) {
				status++
			} else {
				other++
			}
		}
		return status, other
	}
	raw, calls := f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo})
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.status = %s", raw)
	}
	if status, other := lfsCalls(calls); status != 1 || other != 0 {
		t.Errorf("calls with lfs.storage: %d status calls with the item of .gitdir-<n>, %d others, want 1 and 0", status, other)
	}
	_, calls = f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo, "shadowDirOff": true})
	if status, other := lfsCalls(calls); status+other != 0 {
		t.Errorf("with the member %d calls carry lfs.storage, want none", status+other)
	}
}
