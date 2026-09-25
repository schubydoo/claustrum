//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckpointHoldsLeafAndParent pins the Windows share modes of the create's
// checkpoint handles. Both references hold the leaf and its parent open during a
// create on a Windows VM (handle64). Under the handles of f6010b97 a rename of the leaf
// succeeded, a delete-access open of the parent succeeded, and a rename of the
// parent failed with "Access is denied." while the leaf was held inside it. After
// the leaf was moved out, the parent rename succeeded.
func TestCheckpointHoldsLeafAndParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "worktrees")
	leaf := filepath.Join(parent, "w1")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	moved := parent + "-moved"

	// The parent handle alone shares delete, so the parent can be renamed under it.
	h := holdWorktreeDir(parent)
	if h == nil {
		t.Fatal("holdWorktreeDir did not open the parent")
	}
	err := os.Rename(parent, moved)
	if err == nil {
		err = os.Rename(moved, parent)
	}
	_ = h.Close()
	if err != nil {
		t.Fatalf("rename of the parent under its own handle: %v, want it to succeed (the handle shares delete)", err)
	}

	cp := checkpointCreatedWorktree(leaf)
	defer cp.release()
	if cp.info == nil {
		t.Fatal("checkpoint did not capture the leaf")
	}
	if len(cp.held) != 2 {
		t.Fatalf("checkpoint holds %d handles, want 2 (the leaf and its parent)", len(cp.held))
	}
	if err := os.Rename(parent, moved); err == nil {
		t.Fatal("the parent was renamed while the held leaf was inside it, want the rename refused")
	}
	leafMoved := filepath.Join(parent, "w1-moved")
	if err := os.Rename(leaf, leafMoved); err != nil {
		t.Fatalf("rename of the held leaf: %v, want it to succeed (the leaf handle shares delete)", err)
	}
	outside := filepath.Join(root, "w1-out")
	if err := os.Rename(leafMoved, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, moved); err != nil {
		t.Fatalf("rename of the held parent with the leaf moved out: %v, want it to succeed", err)
	}
}

// TestCheckpointCatchesReplacedLeaf pins that, on Windows, a leaf deleted and made
// again at the same path fails the identity check while the checkpoint holds it,
// and that both rollbacks still delete the leaf under the held handles. Before the
// checkpoint took the identity from its leaf handle, os.SameFile read both
// identities from the path and took the replacement for the leaf (measured 5 of 5
// on a Windows VM, where both references refused it).
func TestCheckpointCatchesReplacedLeaf(t *testing.T) {
	t.Run("replaced_leaf_fails_the_check_and_is_removed", func(t *testing.T) {
		leaf := filepath.Join(t.TempDir(), "worktrees", "w1")
		if err := os.MkdirAll(leaf, 0o755); err != nil {
			t.Fatal(err)
		}
		cp := checkpointCreatedWorktree(leaf)
		defer cp.release()
		if cp.info == nil || len(cp.held) != 2 {
			t.Fatalf("checkpoint info %v with %d handles, want the leaf and 2 handles", cp.info, len(cp.held))
		}
		if msg := verifyCreatedWorktree(leaf, cp); msg != "" {
			t.Fatalf("unchanged leaf: verify = %q, want pass", msg)
		}
		if err := os.RemoveAll(leaf); err != nil {
			t.Fatalf("delete of the held leaf: %v", err)
		}
		if err := os.Mkdir(leaf, 0o755); err != nil {
			t.Fatalf("mkdir at the held leaf's path: %v", err)
		}
		if msg := verifyCreatedWorktree(leaf, cp); !strings.Contains(msg, "was not populated by git worktree add") {
			t.Fatalf("replaced leaf: verify = %q, want the not-populated refusal", msg)
		}
		undoFailedAdd(leaf, cp)
		if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
			t.Errorf("the empty replacement survived the add rollback: Lstat err %v", err)
		}
	})

	t.Run("checkout_rollback_deletes_under_the_handles", func(t *testing.T) {
		repo := filepath.Join(t.TempDir(), "repo")
		leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
		admin := fakeRegistration(t, repo, leaf)
		writeFile(t, filepath.Join(leaf, "sub", "f.txt"), "f\n", 0o644)
		cp := checkpointCreatedWorktree(leaf)
		defer cp.release()
		if len(cp.held) != 2 {
			t.Fatalf("checkpoint holds %d handles, want 2", len(cp.held))
		}
		if got := undoFailedCheckout(repo, leaf, "", cp); got != "" {
			t.Errorf("undo text = %q, want none", got)
		}
		for _, p := range []string{admin, leaf} {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				t.Errorf("%s survived the rollback (Lstat err %v)", p, err)
			}
		}
	})
}
