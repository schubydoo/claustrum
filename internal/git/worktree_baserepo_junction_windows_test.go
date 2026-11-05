//go:build windows

package git

import (
	"path/filepath"
	"testing"
)

// A baseRepo <out>\J\..\T, with J a junction to <out>\DEEP\x, names A = <out>\T by
// text, as on b3081b51 (measured on Windows). The remove and the
// create in A act in A, and B = <out>\DEEP\T stays. The remove names the branch, as the
// row does, so the branch goes with the leaf and the registration. A worktreePath in B gets the
// containment refusal, which names baseRepo as sent, and nothing is deleted or made. A
// list_branches path with a junction before its last name lists the branches.
// Mutation: set baseRepoWalksAsSpelled to false in gitdirtrust_windows.go (each row gets
// the trust-root refusal or isRepo false).
func TestBaseRepoJunctionDotDotLexical(t *testing.T) {
	t.Run("remove-in-repo", func(t *testing.T) {
		f := newDotDotJunction(t, makeJunction, true)
		got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove",
			map[string]any{"baseRepo": f.spelled, "worktreePath": f.A.leaf(), "branchName": "w1"}))
		if got != spellRemoveOK {
			t.Errorf("got  %s\nwant %s", got, spellRemoveOK)
		}
		f.A.wantW1(t, "A", false)
		f.B.wantW1(t, "B", true)
	})
	t.Run("create-in-repo", func(t *testing.T) {
		f := newDotDotJunction(t, makeJunction, false)
		got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": f.spelled, "worktreePath": f.A.leaf(), "branchName": "w1"}))
		want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonString(t, f.A.leaf()) +
			`,"sourceBranch":"main","branch":"w1"}}`
		if got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.A.wantW1(t, "A", true)
		f.B.wantW1(t, "B", false)
	})
	t.Run("remove-outside-repo", func(t *testing.T) {
		f := newDotDotJunction(t, makeJunction, true)
		want, _ := f.containmentFrames(t)
		got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove", map[string]any{"baseRepo": f.spelled, "worktreePath": f.B.leaf()}))
		if got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.A.wantW1(t, "A", true)
		f.B.wantW1(t, "B", true)
	})
	t.Run("create-outside-repo", func(t *testing.T) {
		f := newDotDotJunction(t, makeJunction, false)
		_, want := f.containmentFrames(t)
		got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_create",
			map[string]any{"baseRepo": f.spelled, "worktreePath": f.B.leaf(), "branchName": "w1"}))
		if got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.A.wantW1(t, "A", false)
		f.B.wantW1(t, "B", false)
	})
	t.Run("list-branches-junction-at-parent", func(t *testing.T) {
		requireGit(t)
		base := resolveTestRoot(t, t.TempDir())
		initTrustMain(t, filepath.Join(base, "real", "repo"))
		jp := filepath.Join(base, "JP")
		makeJunction(t, jp, filepath.Join(base, "real"))
		resetUserExcludesCache(t)
		clearDaemonConfigEnv(t)
		got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.list_branches", map[string]any{"path": filepath.Join(jp, "repo")}))
		if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"branches":["main"]}}`; got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
}
