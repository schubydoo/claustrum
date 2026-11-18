//go:build windows

package git

import "testing"

// TestRepoOnShare pins the paths that count as a network share (shareGitDir). The
// share of the row is `\\127.0.0.1\<share>\repo` (4c111e86, measured on Windows).
// The other forms are claustrum's own, not measured.
func TestRepoOnShare(t *testing.T) {
	for _, row := range []struct {
		path string
		want bool
	}{
		{`\\127.0.0.1\s\repo`, true},
		{`//127.0.0.1/s/repo`, true},
		{`\\host\share`, true},
		{`C:\x\repo`, false},
		{`\\?\C:\x\repo`, false},
		{`\\?\UNC\host\share\repo`, false},
		{`\\.\pipe\x`, false},
	} {
		if got := repoOnShare(row.path); got != row.want {
			t.Errorf("repoOnShare(%q) = %v, want %v", row.path, got, row.want)
		}
	}
}

// shareTestLink links link to target with a junction (the link row of
// TestShareGitDirThatGitDoesNotUse).
func shareTestLink(t *testing.T, link, target string) {
	t.Helper()
	makeJunction(t, link, target)
}
