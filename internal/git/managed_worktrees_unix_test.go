//go:build unix

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// A marker above the spelling of baseRepo refuses it, also when baseRepo is a
// symbolic link to a repository with no marker above it. Markers 1 and 4 levels up
// were measured (b3081b51 and be1c1ca5, measured on Linux).
func TestBaseRepoSpelledBelowMarkerRefused(t *testing.T) {
	requireGit(t)
	for name, sub := range map[string]string{"1up": "", "4up": filepath.Join("a", "b", "c")} {
		t.Run(name, func(t *testing.T) {
			w := t.TempDir()
			m := filepath.Join(w, "mk")
			if err := os.MkdirAll(filepath.Join(m, sub), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(m, managedWorktreesMarker), "", 0o644)
			r := filepath.Join(w, "out", "R")
			markerLayoutRepo(t, r)
			base := filepath.Join(m, sub, "sl")
			symlinkForTest(t, r, base)
			markerLayoutFrames(t, base, r)
		})
	}
}

// A marker that is a link with no target refuses a baseRepo below it, as a marker
// file does. The create is 4c111e86, measured on Linux. The remove and list_branches
// answers are claustrum's own, not measured.
func TestBaseRepoBelowDanglingMarkerLinkRefused(t *testing.T) {
	requireGit(t)
	m := t.TempDir()
	symlinkForTest(t, "nowhere-marker", filepath.Join(m, managedWorktreesMarker))
	r := filepath.Join(m, "R")
	markerLayoutRepo(t, r)
	markerLayoutFrames(t, r, r)
}

// A marker beside a `.git` link with no target does not count, and the walk goes on
// up. The create and the remove with worktreeRoot outside the marker folder both
// succeed. The remove deletes the worktree pre, its registration and its branch
// (4c111e86, measured on macOS, the create also on Linux).
func TestBaseRepoMarkerBesideDanglingGitLinkAccepted(t *testing.T) {
	requireGit(t)
	x, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := filepath.Join(x, "M")
	if err := os.Mkdir(m, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(m, managedWorktreesMarker), "x\n", 0o644)
	symlinkForTest(t, filepath.Join(x, "nowhere"), filepath.Join(m, ".git"))
	r := filepath.Join(m, "repo")
	initTrustMain(t, r)
	root := filepath.Join(x, "rootout")
	pre := filepath.Join(root, "proj", "pre")
	runGit(t, r, "worktree", "add", "-q", "-b", "pre", pre)
	h := newTestGitDeps()
	w1 := filepath.Join(root, "projc", "w1")
	if got, want := gitDispatch(t, h, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": r, "branchName": "w1", "worktreePath": w1, "worktreeRoot": root})),
		`{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":`+jsonString(t, w1)+`,"sourceBranch":"main","branch":"w1"}}`; got != want {
		t.Errorf("worktree_create = %s\nwant %s", got, want)
	}
	if got, want := gitDispatch(t, h, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": r, "worktreePath": pre, "branchName": "pre", "worktreeRoot": root})),
		`{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
		t.Errorf("worktree_remove = %s\nwant %s", got, want)
	}
	mustBeGone(t, pre)
	mustBeGone(t, filepath.Join(r, ".git", "worktrees", "pre"))
	if got := gitOut(t, r, "branch", "--list", "pre"); got != "" {
		t.Errorf("branch pre = %q, want it deleted", got)
	}
	for _, p := range []string{filepath.Join(m, managedWorktreesMarker), filepath.Join(m, ".git"),
		filepath.Join(w1, ".git"), filepath.Join(r, ".git", "worktrees", "w1"), filepath.Join(root, "proj")} {
		mustExist(t, p)
	}
	if got := gitOut(t, r, "branch", "--list", "main"); got == "" {
		t.Error("branch main is gone, want it kept")
	}
}

// The Linux create row on 4c111e86: a marker file beside a `.git` link with no
// target does not count, so a create from M/B into B/.claude/worktrees succeeds.
func TestBaseRepoMarkerBesideDanglingGitLinkCreatesInRepo(t *testing.T) {
	requireGit(t)
	m := t.TempDir()
	writeFile(t, filepath.Join(m, managedWorktreesMarker), "marker\n", 0o644)
	symlinkForTest(t, "nowhere-git", filepath.Join(m, ".git"))
	b := filepath.Join(m, "B")
	initTrustMain(t, b)
	w1 := filepath.Join(b, ".claude", "worktrees", "w1")
	if got, want := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": b, "branchName": "w1", "worktreePath": w1, "sourceBranch": "main"})),
		`{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":`+jsonString(t, w1)+`,"sourceBranch":"main","branch":"w1"}}`; got != want {
		t.Errorf("worktree_create = %s\nwant %s", got, want)
	}
	mustExist(t, filepath.Join(w1, ".git"))
}
