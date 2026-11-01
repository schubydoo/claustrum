package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// plantStagingEntry makes a folder with one file in the git directory of the
// fixture, with a write time that is age old.
func plantStagingEntry(t *testing.T, gitDir, name string, age time.Duration) string {
	t.Helper()
	return plantStagingEntryAt(t, gitDir, name, time.Now().Add(-age))
}

// plantStagingEntryAt makes a folder with one file in the git directory of the
// fixture, with the write time at.
func plantStagingEntryAt(t *testing.T, gitDir, name string, at time.Time) string {
	t.Helper()
	p := filepath.Join(gitDir, name)
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(p, "f.txt"), "f\n")
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

// commonDirOf is the GIT_COMMON_DIR value of the first call of calls whose argv
// has every word of words, in that order and next to each other.
func commonDirOf(t *testing.T, calls []envCall, words ...string) string {
	t.Helper()
	for _, c := range calls {
		for i := 0; i+len(words) <= len(c.argv); i++ {
			if slices.Equal(c.argv[i:i+len(words)], words) {
				v, _ := envValue(c.env, "GIT_COMMON_DIR")
				return v
			}
		}
	}
	t.Fatalf("no call with %q", words)
	return ""
}

// TestCreateStagesTheAddOnWindowsOnly runs one git.worktree_create in the state
// shadow through the server, with the git stub in front of the git of the
// host, and pins what differs per system.
//
// On Windows a run must match cells 44a and 49a of the Windows VM rounds, and
// control M1 of cell 48: the `worktree add` call carries
// GIT_COMMON_DIR=<S>\.s<n>. On Linux and macOS the call carries the copy of
// the repository. On every system the old entry `.claude-new-worktree-old1` is
// gone with its file (be1c1ca5 on Linux and macOS), the `read-tree` carries the
// copy, the young entry stays, no `.s<n>` folder is left, and the `.git` file
// of the new worktree names the registration in the git directory.
//
// On Linux a build with shadowStagesAdd set fails here: the call carries
// `.s<n>`. Only a Windows run checks the Windows side.
func TestCreateStagesTheAddOnWindowsOnly(t *testing.T) {
	f := newSeededShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	old := plantStagingEntry(t, f.gitDir, ".claude-new-worktree-old1", 2*time.Hour)
	young := plantStagingEntry(t, f.gitDir, ".claude-new-worktree-old2", 5*time.Minute)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if !strings.Contains(raw, `"success":true`) || !strings.Contains(raw, `"branch":"w1"`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	folders := daemonFolders(t, f.run)
	if len(folders) != 1 {
		t.Fatalf("the daemon folders = %q, want one", folders)
	}
	folder := filepath.Join(f.run, shadowRootName, folders[0])
	copyDir := filepath.Join(folder, shadowRepoName(f.gitDir))

	add := commonDirOf(t, calls, "worktree", "add")
	_, oldErr := os.Lstat(old)
	wantText := "gitdir: " + filepath.Join(f.gitDir, "worktrees", "w1") + "\n"
	if runtime.GOOS == "windows" {
		if dir, name := filepath.Split(add); filepath.Clean(dir) != folder || !regexp.MustCompile(`^\.s[0-9]{1,10}$`).MatchString(name) {
			t.Errorf("the add call has GIT_COMMON_DIR=%s, want .s<n> in %s", add, folder)
		}
		wantText = worktreeGitFileText(f.gitDir, "w1")
	} else if add != copyDir {
		t.Errorf("the add call has GIT_COMMON_DIR=%s, want the copy %s", add, copyDir)
	}
	if !os.IsNotExist(oldErr) {
		t.Errorf("the old staging entry: %v, want it gone", oldErr)
	}
	if got := commonDirOf(t, calls, "read-tree"); got != copyDir {
		t.Errorf("the read-tree call has GIT_COMMON_DIR=%s, want the copy %s", got, copyDir)
	}
	if _, err := os.Lstat(filepath.Join(young, "f.txt")); err != nil {
		t.Errorf("the young staging entry: %v, want it kept", err)
	}
	if got := addFolders(t, folder); len(got) != 0 {
		t.Errorf("the folder of the daemon still holds %q", got)
	}
	if got := mustRead(t, filepath.Join(leaf, ".git")); got != wantText {
		t.Errorf("the .git file of the new worktree = %q, want %q", got, wantText)
	}
	for _, name := range []string{"HEAD", "commondir", "gitdir", "index"} {
		if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees", "w1", name)); err != nil {
			t.Errorf("the registration has no %s: %v", name, err)
		}
	}
}
