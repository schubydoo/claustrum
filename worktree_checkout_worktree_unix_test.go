//go:build unix

package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The read-tree checkout of git.worktree_create names the leaf with its symlinks
// resolved in --work-tree. f6010b97 and 89cb6289 do so on a Linux VM for a baseRepo
// sent through a symlink (row CSc). Mutation: pass the leaf as sent.
func TestWorktreeCheckoutWorkTreeIsResolved(t *testing.T) {
	f := newListingFixture(t)
	link := filepath.Join(filepath.Dir(f.top), "lnk")
	symlink(t, f.top, link)
	leaf := filepath.Join(link, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": link, "worktreePath": leaf, "branchName": "w1"})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("create = %s", raw)
	}
	resolved, err := filepath.EvalSymlinks(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == leaf {
		t.Fatalf("the fixture leaf %s holds no symlink", leaf)
	}
	i := slices.IndexFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "read-tree") })
	if i < 0 {
		t.Fatalf("calls = %v, want a read-tree", calls)
	}
	if !slices.Contains(calls[i].argv, "--work-tree="+resolved) {
		t.Errorf("read-tree argv = %q, want --work-tree=%s", calls[i].argv, resolved)
	}
}
