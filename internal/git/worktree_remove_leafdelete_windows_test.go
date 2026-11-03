//go:build windows

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeleteWorktreeDirStaysInItsParentAfterASwapOnWindows is the Windows form
// of TestDeleteWorktreeDirStaysInItsParentAfterASwap. The test opens the parent
// with openRemoveParent, as a remove does. On Windows that open goes through
// Root.OpenRoot, which shares delete, so the parent folder can move away. A
// junction to the folder T then takes its path. T has an entry with the name of
// the leaf. The delete takes the leaf in the moved folder, and T keeps its
// entry. A delete by the path (os.RemoveAll) follows the junction and empties
// T. From the code, not measured. T is a folder of t.TempDir, so a broken
// delete reaches only that folder.
func TestDeleteWorktreeDirStaysInItsParentAfterASwapOnWindows(t *testing.T) {
	base := t.TempDir()
	parentDir := filepath.Join(base, "worktrees")
	moved := filepath.Join(base, "worktrees-moved")
	outside := filepath.Join(base, "T")
	mustWrite(t, filepath.Join(parentDir, "wt", "file.txt"), "worktree file\n")
	mustWrite(t, filepath.Join(parentDir, "wt", ".git"), "gitdir: /nowhere\n")
	keep := filepath.Join(outside, "wt", "keep.txt")
	mustWrite(t, keep, "a file outside the worktree\n")

	target, err := openRemoveParent(base, "worktrees", "wt")
	if err != nil {
		t.Fatal(err)
	}
	if target.parent == nil {
		t.Fatalf("openRemoveParent(%s, worktrees, wt) opened no parent", base)
	}
	defer target.close()
	if err := os.Rename(parentDir, moved); err != nil {
		t.Fatal(err)
	}
	linkAt(t, parentDir, outside)

	if err := deleteWorktreeDir(target.parent, target.leaf, target.path); err != nil {
		t.Fatalf("deleteWorktreeDir: %v", err)
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "a file outside the worktree\n" {
		t.Errorf("the delete went through the junction at the parent path: %s = (%q, %v), want it kept", keep, got, err)
	}
	mustBeGone(t, filepath.Join(moved, "wt"))
}
