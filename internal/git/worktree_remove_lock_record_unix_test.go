//go:build unix

package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWorktreeRemoveLockedEntryOfAnotherRecord: with no daemon GIT_DIR, a lock is
// seen only by an entry whose gitdir record names the worktree. A locked entry
// whose record names another path, or that has no record, does not stop the
// remove. The worktree folder and the branch go, and that entry stays. Beside an
// unlocked entry that names the worktree, a second locked one that names it too
// stays, and the unlocked one goes (b3081b51, measured on Linux). Mutation: let
// worktreeLockedByPath see the lock of the entry named after the leaf, whatever
// its record says.
func TestWorktreeRemoveLockedEntryOfAnotherRecord(t *testing.T) {
	f := newShadowFixture(t)
	for _, r := range []struct {
		name string
		// stage changes the entry e1 of the registrations folder wd. It returns
		// the entries that must stay and those that must go.
		stage func(t *testing.T, wd, e1 string) (kept, gone []string)
	}{
		{"record of another path", func(t *testing.T, wd, e1 string) ([]string, []string) {
			writeFile(t, filepath.Join(wd, e1, "locked"), "", 0o644)
			writeFile(t, filepath.Join(wd, e1, "gitdir"), "/other/.git\n", 0o644)
			return []string{e1}, nil
		}},
		{"no record", func(t *testing.T, wd, e1 string) ([]string, []string) {
			writeFile(t, filepath.Join(wd, e1, "locked"), "", 0o644)
			if err := os.Remove(filepath.Join(wd, e1, "gitdir")); err != nil {
				t.Fatal(err)
			}
			return []string{e1}, nil
		}},
		{"second locked entry", func(t *testing.T, wd, e1 string) ([]string, []string) {
			if err := os.CopyFS(filepath.Join(wd, "e2"), os.DirFS(filepath.Join(wd, e1))); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(wd, "e2", "locked"), "", 0o644)
			return []string{"e2"}, []string{e1}
		}},
	} {
		base := resolveTestRoot(t, t.TempDir())
		requireTempOutsideCheckout(t, base)
		repo := filepath.Join(base, "R")
		live := filepath.Join(repo, ".claude", "worktrees", "w1")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "init", "-q")
		runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
		runGit(t, repo, "worktree", "add", "-q", "-b", "w1", live)
		wd := filepath.Join(repo, ".git", "worktrees")
		ents := entryNames(t, wd)
		if len(ents) != 1 {
			t.Fatalf("%s: entries %q, want one", r.name, ents)
		}
		kept, gone := r.stage(t, wd, ents[0])

		raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": repo, "branchName": "w1", "worktreePath": live})
		if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; raw != want {
			t.Errorf("%s = %s\nwant %s", r.name, raw, want)
		}
		if _, err := os.Lstat(live); !os.IsNotExist(err) {
			t.Errorf("%s: the worktree folder: %v, want it gone", r.name, err)
		}
		for _, n := range kept {
			if _, err := os.Lstat(filepath.Join(wd, n)); err != nil {
				t.Errorf("%s: entry %s: %v, want it kept", r.name, n, err)
			}
		}
		for _, n := range gone {
			if _, err := os.Lstat(filepath.Join(wd, n)); !os.IsNotExist(err) {
				t.Errorf("%s: entry %s: %v, want it gone", r.name, n, err)
			}
		}
		if branchExists(t, repo, "w1") {
			t.Errorf("%s: branch w1 is kept, want it gone", r.name)
		}
	}
}
