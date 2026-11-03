//go:build unix

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeleteWorktreeDirStaysInItsParentAfterASwap: the leaf delete of a remove
// goes through the parent folder that the remove opened, not through the path.
// After the open, the parent folder moves away and a symbolic link to the folder
// T takes its path. T has an entry with the name of the leaf. The delete takes
// the leaf in the moved folder, and T keeps its entry. A delete by the path
// (os.RemoveAll) follows the link and empties T. From the code, not measured.
// The test is for Linux and macOS: on Windows os.OpenRoot shares no delete
// access, so this fixture cannot rename parentDir. The parent that a remove
// holds on Windows comes from Root.OpenRoot, which shares delete, so that swap
// can happen there. TestDeleteWorktreeDirStaysInItsParentAfterASwapOnWindows
// pins it.
func TestDeleteWorktreeDirStaysInItsParentAfterASwap(t *testing.T) {
	base := t.TempDir()
	parentDir := filepath.Join(base, "worktrees")
	moved := filepath.Join(base, "worktrees-moved")
	outside := filepath.Join(base, "T")
	mustWrite(t, filepath.Join(parentDir, "wt", "file.txt"), "worktree file\n")
	mustWrite(t, filepath.Join(parentDir, "wt", ".git"), "gitdir: /nowhere\n")
	keep := filepath.Join(outside, "wt", "keep.txt")
	mustWrite(t, keep, "a file outside the worktree\n")

	parent, err := os.OpenRoot(parentDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if err := os.Rename(parentDir, moved); err != nil {
		t.Fatal(err)
	}
	linkAt(t, parentDir, outside)

	if err := deleteWorktreeDir(parent, "wt", filepath.Join(parentDir, "wt")); err != nil {
		t.Fatalf("deleteWorktreeDir: %v", err)
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "a file outside the worktree\n" {
		t.Errorf("the delete went through the link at the parent path: %s = (%q, %v), want it kept", keep, got, err)
	}
	mustBeGone(t, filepath.Join(moved, "wt"))
}
