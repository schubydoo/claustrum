package main

import (
	"path/filepath"
	"testing"
)

// filepath.Rel cannot relate a relative path to an absolute root; that error must
// read as "not inside" rather than panic or accept. The equal-path case is "not
// inside" by definition (the repo root stays out of reach of the remove).
func TestPathStrictlyUnderRelError(t *testing.T) {
	root := t.TempDir()
	if pathStrictlyUnder(filepath.Join("rel", "x"), root) {
		t.Error("a relative path must not be reported as strictly under an absolute root")
	}
	if pathStrictlyUnder(root, root) {
		t.Error("the root itself must not be strictly under itself")
	}
}

// TestWorktreePathRefusalTicksLeaf pins the "not inside" text of create and
// remove: the leaf in backticks and the repository as it is (be1c1ca5, measured
// on Linux).
func TestWorktreePathRefusalTicksLeaf(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	leaf := filepath.Join(base, "wt1")
	for _, verb := range []string{"create", "remove"} {
		want := "refusing to " + verb + " worktree: `" + leaf + "` is not inside the repository " + repo +
			"; session worktrees are only created and removed under <repository>/.claude/worktrees"
		if got := worktreePathRefusal(repo, leaf, verb); got != want {
			t.Errorf("%s: got  %q\nwant %q", verb, got, want)
		}
	}
}
