//go:build unix

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// resolveAsFarAsExists returns the canonical path directly when the path exists
// and contains a symlink (the fast path, before any ancestor-splitting).
func TestResolveAsFarAsExistsExistingSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if got, want := resolveAsFarAsExists(link), canonicalPath(real); got != want {
		t.Errorf("resolveAsFarAsExists(link) = %q, want %q", got, want)
	}
}

// TestStaleStepReportsALockedEntryThatStays pins the three results of the stale
// step on Linux and macOS. One stale entry with a `locked` file stays, and the
// step reports it (the pair of a create in shadow runs then). One with no such
// file goes. Two stale entries both stay, and neither counts as locked.
// Mutation: set locked to false after a failed remove (the first row fails).
func TestStaleStepReportsALockedEntryThatStays(t *testing.T) {
	for _, row := range []struct {
		name                     string
		entries                  []string
		lock                     bool
		matched, removed, locked bool
	}{
		{"one locked entry", []string{"old"}, true, true, false, true},
		{"one entry", []string{"old"}, false, true, true, false},
		{"two entries, one locked", []string{"a", "b"}, true, true, false, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			repo := t.TempDir()
			leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
			for _, name := range row.entries {
				mustWrite(t, filepath.Join(repo, ".git", worktreesSubdir, name, "gitdir"), filepath.Join(leaf, ".git")+"\n")
			}
			lockFile := filepath.Join(repo, ".git", worktreesSubdir, row.entries[0], "locked")
			if row.lock {
				mustWrite(t, lockFile, "")
			}
			matched, removed, locked := removeStaleWorktreeEntry(repo, leaf, nil)
			if matched != row.matched || removed != row.removed || locked != row.locked {
				t.Errorf("removeStaleWorktreeEntry = %v, %v, %v; want %v, %v, %v", matched, removed, locked, row.matched, row.removed, row.locked)
			}
			if row.lock {
				mustExist(t, lockFile)
			}
		})
	}
}
