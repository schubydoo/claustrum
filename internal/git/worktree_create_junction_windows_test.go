//go:build windows

package git

import (
	"testing"
)

// On Windows b3081b51 creates a worktree whose path goes through a junction, and
// removes it after (measured on Windows). The worktree, its
// registration and the branch are in the real repository. The gitdir record and the
// .git file of the leaf name the paths with the junction resolved. A remove through
// JP of a live worktree that git made removes all three (measured on Windows).
// Mutation: set leafResolvesByHandle to false in gitdirtrust_windows.go (the create
// answers "the create holds no checkpoint of the new worktree", and the live remove
// keeps the leaf).
func TestWorktreeCreateThroughJunction(t *testing.T) {
	for _, row := range []struct {
		name     string
		toParent bool
	}{{"junction-at-repo", false}, {"junction-at-parent", true}} {
		t.Run(row.name, func(t *testing.T) {
			f := newJunctionLeaf(t, makeJunction, row.toParent)
			if got, want := f.create(t), f.createdFrame(t); got != want {
				t.Fatalf("create\ngot  %s\nwant %s", got, want)
			}
			f.wantDisk(t, true)
			if got := f.remove(t); got != spellRemoveOK {
				t.Errorf("remove\ngot  %s\nwant %s", got, spellRemoveOK)
			}
			f.wantDisk(t, false)
		})
	}
	t.Run("junction-at-parent-live", func(t *testing.T) {
		f := newJunctionLeaf(t, makeJunction, true)
		f.addLive(t)
		if got := f.remove(t); got != spellRemoveOK {
			t.Errorf("remove\ngot  %s\nwant %s", got, spellRemoveOK)
		}
		f.wantDisk(t, false)
	})
}

// A checkout that fails through the junction at the repo rolls the create back, as
// b3081b51 does (measured on Windows). The create holds its git directory, as in the
// state shadow of the daemon. The leaf goes, the registration goes through the held
// folder, and the branch goes. The junction and the real repository stay.
// Mutation: set leafResolvesByHandle to false in gitdirtrust_windows.go (the create
// answers the checkpoint text before the checkout).
func TestWorktreeCreateThroughJunctionRollback(t *testing.T) {
	f := newJunctionLeaf(t, makeJunction, false)
	installGitSlowStub(t, mustLookGit(t))
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
	slowGit(t, "read-tree", "fail", 0, `fatal: synthetic checkout failure\n`, "")
	d, _ := newSeededShadowDeps(t)
	got := gitDispatch(t, d, rpcLine(t, "git.worktree_create",
		map[string]any{"baseRepo": f.baseRepo, "branchName": "w1", "worktreePath": f.leaf}))
	wantError(t, got, "git worktree add failed (checkout): fatal: synthetic checkout failure", "worktree_add_failed")
	f.wantRolledBack(t)
}
