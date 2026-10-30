//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A link that takes the place of `.git` during the settle step gets the answer of a
// swap after the read-tree call: the create fails with the real-directory text,
// and the registration gets no index (claustrum's own, not measured). The last git
// call is the settle call.
func TestCreateWithASwapDuringTheSettleStep(t *testing.T) {
	const realDir = `".git" is a link or a file where Claude needs a real directory; put the directory itself in its place`
	f := newSeededShadowFixture(t)
	setAttrSource(t, true)
	other := filepath.Join(resolveTestRoot(t, t.TempDir()), "T")
	if err := os.CopyFS(other, os.DirFS(f.gitDir)); err != nil {
		t.Fatal(err)
	}
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	swapAt(t, "settled", f.gitDir, other)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if got := createError(t, raw); !strings.HasPrefix(got, "git worktree add failed (checkout): "+realDir) {
		t.Fatalf("the error = %s, want the real-directory text", got)
	}
	if last := calls[len(calls)-1]; !slices.Contains(last.argv, "diff-files") {
		t.Errorf("the last call = %q, want the settle call", last.argv)
	}
	if _, err := os.Lstat(filepath.Join(f.gitDir+"-real", "worktrees", "w1", "index")); !os.IsNotExist(err) {
		t.Errorf("the index of the registration: %v, want none", err)
	}
}
