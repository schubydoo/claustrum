package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Windows walk of a git.status baseRepo drops the name before a ".." first,
// also a missing one: 4c111e86 runs git.status on <S>\missing\..\P\repo as on
// <S>\P\repo (measured on Windows). A missing name with no ".." after it still
// fails the walk. The seam baseRepoWalksAsSpelled runs the Windows walk on every
// system. Mutation: walk without foldFirst in statusBaseWalkFails (the first row
// fails).
func TestStatusBaseWalkFoldsDotDotAfterAMissingName(t *testing.T) {
	old := baseRepoWalksAsSpelled
	t.Cleanup(func() { baseRepoWalksAsSpelled = old })
	baseRepoWalksAsSpelled = true
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "S", "P", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	// filepath.Join would clean the "..", so the paths are joined by hand.
	path := func(names ...string) string {
		return root + string(filepath.Separator) + strings.Join(names, string(filepath.Separator))
	}
	for _, c := range []struct {
		p    string
		fail bool
	}{
		{path("S", "missing", "..", "P", "repo"), false},
		{path("S", "P", "..", "P", "repo"), false},
		{path("S", "missing", "P", "repo"), true},
	} {
		if got := statusBaseWalkFails(c.p); got != c.fail {
			t.Errorf("statusBaseWalkFails(%q) = %t, want %t", c.p, got, c.fail)
		}
	}
}
