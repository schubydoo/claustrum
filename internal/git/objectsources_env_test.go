package git

import (
	"path/filepath"
	"strings"
	"testing"
)

// GIT_ALTERNATE_OBJECT_DIRECTORIES in the daemon's environment refuses git.status
// and git.list_branches with no git call, also with "ownGitConfigOff":true. With
// "shadowDirOff":true both run git (4c111e86, measured on Windows). This test runs
// the checks of Windows on every system.
func TestAlternateEnvRefusalOfStatusAndBranches(t *testing.T) {
	f := newListingFixture(t)
	checksOn(t)
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(t.TempDir(), "objects"))
	refusal := `{"code":-32603,"message":"` + alternateEnvText + `"}`
	for _, row := range []struct {
		method  string
		params  map[string]any
		refused bool
	}{
		{"git.list_branches", map[string]any{"path": f.top}, true},
		{"git.list_branches", map[string]any{"path": f.top, "ownGitConfigOff": true}, true},
		{"git.status", map[string]any{"path": f.top, "baseRepo": f.top, "ownGitConfigOff": true}, true},
		{"git.list_branches", map[string]any{"path": f.top, "shadowDirOff": true}, false},
		{"git.status", map[string]any{"path": f.top, "baseRepo": f.top, "shadowDirOff": true}, false},
	} {
		raw, calls := f.call(t, row.method, row.params)
		got := replyPart(t, raw)
		if row.refused {
			if got != refusal {
				t.Errorf("%s %v: %s, want %s", row.method, row.params, got, refusal)
			}
			for _, c := range calls {
				if !slicesHasExcludesRead(c.argv) {
					t.Errorf("%s %v: git call %q, want none but the excludes read", row.method, row.params, c.argv)
				}
			}
			continue
		}
		if strings.Contains(got, "GIT_ALTERNATE_OBJECT_DIRECTORIES") || len(calls) == 0 {
			t.Errorf("%s %v: %s with %d git calls, want git to run", row.method, row.params, got, len(calls))
		}
	}
}

// slicesHasExcludesRead reports whether argv is the excludes read of git.status,
// which runs before the check (measured on Windows).
func slicesHasExcludesRead(argv []string) bool {
	return strings.Contains(strings.Join(argv, " "), "core.excludesFile")
}
