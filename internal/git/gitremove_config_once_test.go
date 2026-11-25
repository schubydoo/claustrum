package git

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestRemoveReadsARefusedConfigOnceOnWindows: on Windows a remove in a
// repository whose config has a syntax error makes one read of the config and
// one `git version` call, as each other method does. 87375cc7 and 4c111e86
// make 2 calls there (measured on Windows). On Linux and macOS the test sets
// removeRereadsRefusedConfig as Windows has it.
func TestRemoveReadsARefusedConfigOnceOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		old := removeRereadsRefusedConfig
		t.Cleanup(func() { removeRereadsRefusedConfig = old })
		removeRereadsRefusedConfig = false
	}
	f := newSeededShadowFixture(t)
	setRepoConfig(t, f.gitDir, "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n[alias]\n\tx = \"unterminated\n")
	raw, calls := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.wt})
	if !strings.Contains(raw, "this repository's git config cannot be used: ") {
		t.Fatalf("git.worktree_remove = %s", raw)
	}
	var got []string
	for _, c := range calls {
		switch {
		case c.configRead():
			got = append(got, "read")
		case c.plainVersion(t):
			got = append(got, "version")
		case c.isProbe() || c.excludesRead():
		default:
			t.Errorf("the remove made the call %q, want the reads only", c.argv)
		}
	}
	if want := []string{"read", "version"}; !slices.Equal(got, want) {
		t.Errorf("the remove made %q, want %q", got, want)
	}
}
