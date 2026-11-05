//go:build unix

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// goneGitDirCopyText is the refusal of the failed copy under a daemon GIT_DIR that
// names a missing path, for the worktree folder live.
func goneGitDirCopyText(live string) string {
	return "failed to remove worktree: could not check whether " + live + " is locked: Claude could not prepare " +
		"its own files on the remote machine: cannot write its copy of the repository's git config: " +
		"the HEAD of the repository cannot be read"
}

// TestDaemonGitDirToMissingPathRemovesLiveWorktree: with the GIT_DIR of
// TestDaemonGitDirToMissingPath and no `.git` entry in F, a remove of an existing
// worktree folder without worktreeRoot deletes the folder with its files and
// answers success (b3081b51, measured on Linux and macOS). branchKept follows the
// branch name, and no branch step runs. Mutations: drop
// the new branch at the goneCopyText refusal, drop its deleteWorktreeDir call, or
// set branchKept to true.
func TestDaemonGitDirToMissingPathRemovesLiveWorktree(t *testing.T) {
	f := newShadowFixture(t)
	steps := branchStepRuns(t)
	for _, r := range []struct {
		name, branch, want string
	}{
		{"named branch", "w1", `{"success":true,"branchKept":true}`},
		{"empty branch", "", `{"success":true}`},
	} {
		base := resolveTestRoot(t, t.TempDir())
		requireTempOutsideCheckout(t, base)
		dir := filepath.Join(base, "F")
		missing := filepath.Join(base, "nonexist")
		t.Setenv("GIT_DIR", missing)
		live := filepath.Join(dir, ".claude", "worktrees", "w1")
		writeFile(t, filepath.Join(live, "keep.txt"), "k\n", 0o644)
		writeFile(t, filepath.Join(live, "sub", "x"), "x\n", 0o644)
		raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": dir, "branchName": r.branch, "worktreePath": live})
		if want := `{"jsonrpc":"2.0","id":1,"result":` + r.want + `}`; raw != want {
			t.Errorf("%s = %s\nwant %s", r.name, raw, want)
		}
		if _, err := os.Lstat(live); !os.IsNotExist(err) {
			t.Errorf("%s: the worktree folder: %v, want it gone", r.name, err)
		}
		if got := entryNames(t, filepath.Join(dir, ".claude", "worktrees")); len(got) != 0 {
			t.Errorf("%s: .claude/worktrees holds %q, want nothing", r.name, got)
		}
		if _, err := os.Lstat(missing); !os.IsNotExist(err) {
			t.Errorf("%s: the GIT_DIR path: %v, want it missing", r.name, err)
		}
		if n := steps.Load(); n != 0 {
			t.Errorf("%s: the branch step ran %d times, want 0", r.name, n)
		}
	}
}

// TestDaemonGitDirToMissingPathRemoveKeepsGuards: with the GIT_DIR of
// TestDaemonGitDirToMissingPath, each guard before the delete of an existing
// worktree folder still refuses, and nothing is deleted. A `.git` link or a
// regular `.git` file in F keeps the refusal of the failed copy (claustrum's
// own, not measured). Every path lies in the temp folder of the test.
// Mutations: drop the dotGitAbsentOrFolder test, let a regular file pass it,
// or drop the identity test in deleteWorktreeDir.
func TestDaemonGitDirToMissingPathRemoveKeepsGuards(t *testing.T) {
	f := newShadowFixture(t)
	for _, r := range []struct {
		name string
		// stage makes the state in F around the worktree folder live. It returns
		// the folder whose files must stay, and the refusal text or its start.
		stage func(t *testing.T, dir, live string) (kept, want string)
	}{
		{"dot git link", func(t *testing.T, dir, live string) (string, string) {
			writeFile(t, filepath.Join(live, "keep.txt"), "k\n", 0o644)
			if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, ".git")); err != nil {
				t.Fatal(err)
			}
			return live, goneGitDirCopyText(live)
		}},
		{"dot git file", func(t *testing.T, dir, live string) (string, string) {
			writeFile(t, filepath.Join(live, "keep.txt"), "k\n", 0o644)
			writeFile(t, filepath.Join(dir, ".git"), "gitdir: "+filepath.Join(dir, "nowhere")+"\n", 0o644)
			return live, goneGitDirCopyText(live)
		}},
		{"leaf link", func(t *testing.T, dir, live string) (string, string) {
			other := filepath.Join(dir, "other")
			writeFile(t, filepath.Join(other, "keep.txt"), "k\n", 0o644)
			if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other, live); err != nil {
				t.Fatal(err)
			}
			return other, "refusing to remove worktree: " + live + " is a symbolic link, not a worktree directory"
		}},
		{"claude link", func(t *testing.T, dir, live string) (string, string) {
			other := filepath.Join(filepath.Dir(dir), "other")
			writeFile(t, filepath.Join(other, "worktrees", "w1", "keep.txt"), "k\n", 0o644)
			if err := os.Symlink(other, filepath.Join(dir, ".claude")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(other, "worktrees", "w1"), "refusing to remove worktree: " + filepath.Join(dir, ".claude") +
				" is a symbolic link; "
		}},
		{"home inside", func(t *testing.T, dir, live string) (string, string) {
			writeFile(t, filepath.Join(live, "keep.txt"), "k\n", 0o644)
			home := filepath.Join(live, "home")
			if err := os.Mkdir(home, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			return live, "worktreePath must not be or contain the home directory: \"" + live + "\""
		}},
		{"leaf swap", func(t *testing.T, dir, live string) (string, string) {
			writeFile(t, filepath.Join(live, "keep.txt"), "k\n", 0o644)
			moved := live + ".old"
			leafOpenedHook = func() {
				if err := os.Rename(live, moved); err != nil {
					t.Error(err)
				}
				writeFile(t, filepath.Join(live, "keep.txt"), "k\n", 0o644)
			}
			t.Cleanup(func() { leafOpenedHook = nil })
			return moved, "refusing to remove worktree: " + live + " changed while the removal was checking it"
		}},
	} {
		t.Run(r.name, func(t *testing.T) {
			base := resolveTestRoot(t, t.TempDir())
			requireTempOutsideCheckout(t, base)
			dir := filepath.Join(base, "F")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_DIR", filepath.Join(base, "nonexist"))
			live := filepath.Join(dir, ".claude", "worktrees", "w1")
			kept, want := r.stage(t, dir, live)
			raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": dir, "branchName": "w1", "worktreePath": live})
			if w := refusalFrame(t, want); raw != w && !strings.HasPrefix(raw, strings.TrimSuffix(w, `"}}`)) {
				t.Errorf("remove = %s\nwant %s", raw, w)
			}
			if got := entryNames(t, kept); !slices.Contains(got, "keep.txt") {
				t.Errorf("%s holds %q, want keep.txt", kept, got)
			}
		})
	}
}

// TestDaemonGitDirToMissingPathRemoveDotGitFolder: with the GIT_DIR of
// TestDaemonGitDirToMissingPath and a `.git` folder in F that registers w1, a
// remove of the existing worktree folder deletes it when no lock names it
// (b3081b51, measured on Linux). A `locked` file there keeps the D22 refusal, and
// nothing is deleted. Every path lies in the temp folder of the test. Mutations:
// drop the baseRepoLockRefusal call of the new branch (locked deletes), or take
// a `.git` folder out of dotGitAbsentOrFolder (unlocked refuses).
func TestDaemonGitDirToMissingPathRemoveDotGitFolder(t *testing.T) {
	f := newShadowFixture(t)
	for _, r := range []struct {
		name   string
		locked bool
	}{
		{"unlocked", false},
		{"locked", true},
	} {
		t.Run(r.name, func(t *testing.T) {
			base := resolveTestRoot(t, t.TempDir())
			requireTempOutsideCheckout(t, base)
			dir := filepath.Join(base, "F")
			t.Setenv("GIT_DIR", filepath.Join(base, "nonexist"))
			live := filepath.Join(dir, ".claude", "worktrees", "w1")
			entry := filepath.Join(dir, ".git", "worktrees", "w1")
			writeFile(t, filepath.Join(live, "keep.txt"), "k\n", 0o644)
			writeFile(t, filepath.Join(live, ".git"), "gitdir: "+entry+"\n", 0o644)
			writeFile(t, filepath.Join(entry, "gitdir"), filepath.Join(live, ".git")+"\n", 0o644)
			if r.locked {
				writeFile(t, filepath.Join(entry, "locked"), "", 0o644)
			}
			raw, _ := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": dir, "branchName": "w1", "worktreePath": live})
			if !r.locked {
				if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`; raw != want {
					t.Errorf("remove = %s\nwant %s", raw, want)
				}
				if _, err := os.Lstat(live); !os.IsNotExist(err) {
					t.Errorf("the worktree folder: %v, want it gone", err)
				}
				return
			}
			if want := refusalFrame(t, lockedWorktreeRefusal(live, lockedPlain)); raw != want {
				t.Errorf("remove = %s\nwant %s", raw, want)
			}
			if got := entryNames(t, live); !slices.Contains(got, "keep.txt") || !slices.Contains(got, ".git") {
				t.Errorf("%s holds %q, want keep.txt and .git", live, got)
			}
			if got := entryNames(t, entry); !slices.Contains(got, "locked") {
				t.Errorf("%s holds %q, want locked", entry, got)
			}
		})
	}
}
