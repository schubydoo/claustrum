package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUnusableGitDirHeadOpenTakesNoTerminal pins that the HEAD open of
// gitDirUnusable gives its process no controlling terminal. The git directory has
// no objects and no refs, so the test reaches that open. Its HEAD is a link to a
// pty slave. The open runs in a child that leads a new session, as the daemon does
// (see TestRepoConfigOpenTakesNoTerminal). Claustrum's own, not measured.
func TestUnusableGitDirHeadOpenTakesNoTerminal(t *testing.T) {
	g := filepath.Join(t.TempDir(), ".git")
	if err := os.Mkdir(g, 0o700); err != nil {
		t.Fatal(err)
	}
	defer linkPtySlave(t, filepath.Join(g, "HEAD"))()
	if got := childTerminal(t, "git-dir-unusable", g); got != "no terminal" {
		t.Errorf("after the HEAD open of a pty slave the child has %q, want no terminal", got)
	}
}
