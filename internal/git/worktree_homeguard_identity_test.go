package git

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The home guard of the worktree leaf deletes compares folders by identity too
// (D2). Each test sets HOME to a folder inside a t.TempDir() and reaches
// it by a spelling that the text test lets pass. Against the tree without the
// identity test, the deletes hit that temporary folder only, never a real home.

// git.worktree_remove refuses a leaf that is home or holds home by identity, with
// the text of the home guard, and deletes nothing.
func TestWorktreeRemoveRefusesHomeByIdentity(t *testing.T) {
	requireGit(t)
	for _, tc := range []struct {
		name string
		// setup gives the baseRepo and the worktreePath to send, and sets HOME.
		setup func(t *testing.T, top, r, w2 string) (base, sent string)
	}{
		// baseRepo through a symbolic link, and HOME is the leaf by its real path.
		{"leaf is home, baseRepo through a link", func(t *testing.T, top, r, w2 string) (string, string) {
			link := filepath.Join(top, "L")
			symlinkOrSkip(t, r, link)
			t.Setenv(homeEnvVar(), w2)
			return link, filepath.Join(link, ".claude", "worktrees", "w2")
		}},
		// HOME is a folder in the leaf. Its text does not start with the path sent.
		{"leaf holds home, baseRepo through a link", func(t *testing.T, top, r, w2 string) (string, string) {
			link := filepath.Join(top, "L")
			symlinkOrSkip(t, r, link)
			inner := filepath.Join(w2, "inner")
			if err := os.Mkdir(inner, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv(homeEnvVar(), inner)
			return link, filepath.Join(link, ".claude", "worktrees", "w2")
		}},
		// HOME in another letter case. It runs only on a volume that ignores the
		// case, as the default volume of macOS does.
		{"leaf is home in another letter case", func(t *testing.T, top, r, w2 string) (string, string) {
			upper := filepath.Join(filepath.Dir(w2), "W2")
			a, errA := os.Stat(upper)
			b, errB := os.Stat(w2)
			if errA != nil || errB != nil || !os.SameFile(a, b) {
				t.Skip("the volume tells letter case apart")
			}
			t.Setenv(homeEnvVar(), upper)
			return r, w2
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			top := t.TempDir()
			r := filepath.Join(top, "R")
			markerLayoutRepo(t, r)
			w2 := filepath.Join(r, ".claude", "worktrees", "w2")
			keep := filepath.Join(w2, "KEEP.txt")
			writeFile(t, keep, "must survive", 0o600)
			base, sent := tc.setup(t, top, r, w2)
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
				jsonString(t, fmt.Sprintf("worktreePath must not be or contain the home directory: %q", sent)) + `}}`
			got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_remove",
				map[string]any{"baseRepo": base, "worktreePath": sent, "branchName": "w2"}))
			if got != want {
				t.Errorf("worktree_remove(%s) = %s\nwant %s", sent, got, want)
			}
			mustExist(t, keep)
			mustExist(t, filepath.Join(r, ".git", "worktrees", "w2"))
		})
	}
}

// The rollbacks of git.worktree_create keep a leaf that is home by identity. HOME
// is a symbolic link to the leaf, so the text test lets the leaf pass.
func TestWorktreeRollbackRefusesHomeByIdentity(t *testing.T) {
	setup := func(t *testing.T, withFile bool) (repo, wt string, cp worktreeCheckpoint) {
		t.Helper()
		top := t.TempDir()
		repo = filepath.Join(top, "repo")
		wt = filepath.Join(repo, ".claude", "worktrees", "wt")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		if withFile {
			writeFile(t, filepath.Join(wt, "KEEP.txt"), "must survive", 0o600)
		}
		symlinkOrSkip(t, wt, filepath.Join(top, "alias"))
		cp = checkpointCreatedWorktree(wt)
		t.Cleanup(cp.release)
		if cp.info == nil || cp.parentInfo == nil {
			t.Fatal("checkpoint did not capture the leaf")
		}
		t.Setenv(homeEnvVar(), filepath.Join(top, "alias"))
		return repo, wt, cp
	}

	// An empty leaf passes the rmdir, so only the guard keeps it.
	t.Run("undoFailedAdd", func(t *testing.T) {
		_, wt, cp := setup(t, false)
		undoFailedAdd(wt, cp)
		mustExist(t, wt)
	})

	t.Run("undoFailedCheckout", func(t *testing.T) {
		repo, wt, cp := setup(t, true)
		if got, _ := undoFailedCheckout(gitUse{}, repo, wt, "", cp, testedRegistration{noRegistration: true}); got != "" {
			t.Errorf("undo text = %q, want none", got)
		}
		mustExist(t, filepath.Join(wt, "KEEP.txt"))
	})

	t.Run("removeCreatedLeaf", func(t *testing.T) {
		_, wt, cp := setup(t, true)
		removeCreatedLeaf(wt, cp)
		mustExist(t, filepath.Join(wt, "KEEP.txt"))
	})
}

// The rollbacks keep a leaf that holds home by identity. HOME is a folder in the
// leaf, reached through a symbolic link to the leaf, so its text does not start
// with the leaf path.
func TestWorktreeRollbackKeepsALeafThatHoldsHome(t *testing.T) {
	setup := func(t *testing.T) (repo, wt, keep string, cp worktreeCheckpoint) {
		t.Helper()
		top := t.TempDir()
		repo = filepath.Join(top, "repo")
		wt = filepath.Join(repo, ".claude", "worktrees", "wt")
		keep = filepath.Join(wt, "inner", "KEEP.txt")
		writeFile(t, keep, "must survive", 0o600)
		alias := filepath.Join(top, "alias")
		symlinkOrSkip(t, wt, alias)
		cp = checkpointCreatedWorktree(wt)
		t.Cleanup(cp.release)
		if cp.info == nil || cp.parentInfo == nil {
			t.Fatal("checkpoint did not capture the leaf")
		}
		t.Setenv(homeEnvVar(), filepath.Join(alias, "inner"))
		return repo, wt, keep, cp
	}

	t.Run("undoFailedCheckout", func(t *testing.T) {
		repo, wt, keep, cp := setup(t)
		if got, _ := undoFailedCheckout(gitUse{}, repo, wt, "", cp, testedRegistration{noRegistration: true}); got != "" {
			t.Errorf("undo text = %q, want none", got)
		}
		mustExist(t, keep)
	})

	t.Run("removeCreatedLeaf", func(t *testing.T) {
		_, wt, keep, cp := setup(t)
		removeCreatedLeaf(wt, cp)
		mustExist(t, keep)
	})
}
