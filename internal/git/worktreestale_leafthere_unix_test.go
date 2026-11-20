//go:build unix

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// The stale step removes no entry when the leaf is there at its remove. A
// create of the same leaf that started after the leaf test of this request may
// own that entry (claustrum's own, not measured). The control: with no leaf the
// one stale entry goes. Mutation: drop the Lstat of the leaf in
// removeStaleWorktreeEntry.
func TestStaleStepKeepsTheEntryOfALeafThatIsThere(t *testing.T) {
	for _, tc := range []struct {
		name      string
		leafThere bool
	}{{"leaf there", true}, {"no leaf", false}} {
		t.Run(tc.name, func(t *testing.T) {
			repo := filepath.Join(realTempDir(t), "R")
			leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
			entry := filepath.Join(repo, ".git", "worktrees", "w1")
			writeFile(t, filepath.Join(entry, "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
			writeFile(t, filepath.Join(entry, "HEAD"), "ref: refs/heads/b1\n", 0o644)
			if tc.leafThere {
				mkdirForTest(t, leaf)
			}
			matched, removed, _ := removeStaleWorktreeEntry(repo, leaf, nil)
			_, err := os.Lstat(entry)
			if !matched || removed == tc.leafThere || (err == nil) != tc.leafThere {
				t.Errorf("matched %v, removed %v, entry Lstat error %v: want the entry kept only with the leaf there", matched, removed, err)
			}
		})
	}
}
