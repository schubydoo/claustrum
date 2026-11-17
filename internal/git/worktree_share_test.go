package git

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestShareGitDirThatGitDoesNotUse runs a create and a remove where git finds no
// repository in a folder that the trust check accepted, and repoOnShare counts
// baseRepo as a network share. On Windows 4c111e86 answers the create with
// unusableGitText for `<baseRepo>\.git`, and a remove of a gone worktree with
// success and a kept branch (measured on a share). Nothing is made or deleted.
// A lock that names the gone worktree keeps the D22 refusal, and a worktree that
// exists keeps the lock-check refusal (claustrum's own, not measured). A folder
// that is not on a share keeps not_a_repo and the lock-check refusal.
func TestShareGitDirThatGitDoesNotUse(t *testing.T) {
	const dubious = "fatal: detected dubious ownership in repository at '//127.0.0.1/s/repo'\n"
	for _, row := range []struct {
		name        string
		share, gone bool
		locked      bool
		method      string
		want        func(f shadowFixture, leaf string) string
	}{
		{"create on a share", true, true, false, "git.worktree_create", func(f shadowFixture, _ string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
				jsonQuoted(t, fmt.Sprintf(unusableGitText, filepath.Join(f.repo, ".git"))) + `,"errorCode":"worktree_add_failed"}}`
		}},
		{"create on a local folder", false, true, false, "git.worktree_create", func(shadowFixture, string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`
		}},
		{"remove of a gone worktree on a share", true, true, false, "git.worktree_remove", func(shadowFixture, string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
		}},
		{"remove of a gone locked worktree on a share", true, true, true, "git.worktree_remove", func(_ shadowFixture, leaf string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonQuoted(t, goneLockedWorktreeRefusal(leaf, lockedPlain)) + `}}`
		}},
		{"remove of a worktree that exists on a share", true, false, false, "git.worktree_remove", func(_ shadowFixture, leaf string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonQuoted(t, lockCheckRefusal(leaf)) + `}}`
		}},
		{"remove of a gone worktree on a local folder", false, true, false, "git.worktree_remove", func(_ shadowFixture, leaf string) string {
			return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonQuoted(t, lockCheckRefusal(leaf)) + `}}`
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			old := repoOnShare
			repoOnShare = func(repo string) bool { return row.share && repo == f.repo }
			t.Cleanup(func() { repoOnShare = old })
			answerProbe(t, 128, "", dubious)
			answerSecondProbe(t, 0, "")
			leaf := f.wt
			if row.gone {
				leaf = filepath.Join(f.repo, ".claude", "worktrees", "w1")
			}
			entry := filepath.Join(f.gitDir, "worktrees", "w1")
			if row.locked {
				writeFile(t, filepath.Join(entry, "gitdir"), filepath.Join(leaf, ".git")+"\n", 0o644)
				writeFile(t, filepath.Join(entry, "locked"), "held\n", 0o644)
			}
			raw, _ := f.call(t, row.method, map[string]any{"baseRepo": f.repo, "worktreePath": leaf, "branchName": "w1"})
			if want := row.want(f, leaf); raw != want {
				t.Errorf("%s = %s\nwant %s", row.method, raw, want)
			}
			for _, p := range []string{f.wt, filepath.Join(f.gitDir, "worktrees", "wr")} {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("the fixture: %v", err)
				}
			}
			if row.gone {
				if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
					t.Errorf("the gone worktree: %v, want none", err)
				}
			}
			if row.locked {
				if _, err := os.Stat(filepath.Join(entry, "locked")); err != nil {
					t.Errorf("the locked registration: %v", err)
				}
			}
		})
	}
}
