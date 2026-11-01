package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestStagedEntryTakesTheGroupOfWorktrees: on Linux a `worktrees` folder of a
// second group of the user, with mode 2775, gives the new entry and its files
// that group. be1c1ca5 does so on Linux (measured). The test runs only where
// the user has a second group. Without chownStagedTree the entry keeps the
// group of `.git`.
func TestStagedEntryTakesTheGroupOfWorktrees(t *testing.T) {
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	second := -1
	for _, g := range groups {
		if g != os.Getgid() {
			second = g
			break
		}
	}
	if second < 0 {
		t.Skip("the user has no second group")
	}
	f := newSeededShadowFixture(t)
	worktrees := filepath.Join(f.gitDir, "worktrees")
	if err := os.Chown(worktrees, -1, second); err != nil {
		t.Skip("cannot give worktrees the second group: ", err)
	}
	if err := os.Chmod(worktrees, 0o775|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	if raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	reg := filepath.Join(worktrees, "w1")
	for _, p := range []string{reg, filepath.Join(reg, "HEAD"), filepath.Join(reg, "gitdir")} {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if gid := int(fi.Sys().(*syscall.Stat_t).Gid); gid != second {
			t.Errorf("%s has group %d, want %d", p, gid, second)
		}
	}
}
