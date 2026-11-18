package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateOnMissingBaseRepoRunsNoStartTest pins a git.worktree_create on a
// baseRepo that does not exist, as the first requests of a daemon with a run
// folder. The rows of 4c111e86 have not_a_repo, no state line and no start test
// on Linux, macOS and Windows. On Windows the first such request makes one git
// call and the second none. Here the one call is the read of the excludes file.
func TestCreateOnMissingBaseRepoRunsNoStartTest(t *testing.T) {
	f := newShadowFixture(t)
	logs := captureLogBuf(t)
	root := resolveTestRoot(t, t.TempDir())
	marked := filepath.Join(root, "M")
	plain := filepath.Join(root, "P")
	for _, d := range []string{marked, plain} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(marked, managedWorktreesMarker), "", 0o644)

	const want = `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`
	for i, base := range []string{filepath.Join(marked, "missing"), filepath.Join(plain, "missing")} {
		raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": base, "branchName": "w1"})
		if raw != want {
			t.Errorf("create %d on %s = %s\nwant %s", i, base, raw, want)
		}
		var other []string
		for _, c := range calls {
			if i > 0 || !strings.Contains(strings.Join(c.argv, " "), "core.excludesFile") {
				other = append(other, strings.Join(c.argv, " "))
			}
		}
		if len(other) != 0 {
			t.Errorf("create %d on %s made git calls %q, want none but the first excludes read", i, base, other)
		}
	}
	if n := stateLines(logs.String()); n != 0 {
		t.Errorf("the creates logged %d state lines, want none", n)
	}
	// The start test makes the folder of the daemon and its `.check-<n>` folder.
	if names := daemonFolders(t, f.run); names != nil {
		t.Errorf("the creates made git-shadow: %v", names)
	}
}
