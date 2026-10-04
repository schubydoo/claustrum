//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// makeJunction links link to target with mklink /J, or skips the test when the
// junction cannot be made.
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J %s %s: %v\n%s", link, target, err, out)
	}
}

// git.worktree_create refuses a junctioned .claude or .claude\worktrees at the parent
// step and creates nothing: no directory in the junction target, no entry and no
// branch. Measured against f6010b97 on a Windows VM (rows JCR1 and JCR2).
func TestWorktreeCreateRefusesJunctionParent(t *testing.T) {
	for _, c := range []struct{ name, junction string }{
		{"claude", ".claude"},
		{"worktrees", filepath.Join(".claude", "worktrees")},
	} {
		t.Run(c.name, func(t *testing.T) {
			requireGit(t)
			base := resolveTestRoot(t, t.TempDir())
			repo := filepath.Join(base, "repo")
			copyFixtureTemplate(t, "worktree-remove", repo, func(t *testing.T, dir string) {
				runGit(t, dir, "init", "-q", "-b", "main")
				runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
			})
			if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			if c.name == "claude" {
				if err := os.Remove(filepath.Join(repo, ".claude")); err != nil {
					t.Fatal(err)
				}
			}
			outside := filepath.Join(base, "outside")
			makeJunction(t, filepath.Join(repo, c.junction), outside)
			wp := filepath.Join(repo, ".claude", "worktrees", "wt1")
			raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.worktree_create",
				map[string]any{"baseRepo": repo, "branchName": "wt1", "worktreePath": wp}))
			want := "failed to create parent directory: " + filepath.Join(repo, c.junction) + " is not a directory"
			if got := removeErrorField(t, raw); got != want || !strings.Contains(raw, `"errorCode":"mkdir_failed"`) {
				t.Errorf("reply = %s\nwant error %q with errorCode mkdir_failed", raw, want)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Errorf("the junction target holds %v (err=%v), want it empty", entries, err)
			}
			mustBeGone(t, filepath.Join(repo, ".git", "worktrees"))
			if gitExitOK(repo, "show-ref", "--verify", "--quiet", "refs/heads/wt1") {
				t.Error("branch wt1 was created")
			}
		})
	}
}

// shortPathOf returns the 8.3 spelling of the existing path p, or skips the test when
// the volume keeps no short names.
func shortPathOf(t *testing.T, p string) string {
	t.Helper()
	pUTF16, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(pUTF16, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		t.Skipf("GetShortPathName(%s): n=%d err=%v", p, n, err)
	}
	s := windows.UTF16ToString(buf[:n])
	if filepath.Base(s) == filepath.Base(p) {
		t.Skip("8.3 short names are disabled on this volume")
	}
	return s
}

// A gone worktree whose parent directory is gone too, with a locked registration, is
// refused when baseRepo is sent in its 8.3 short form. The branch stays. Measured
// against f6010b97 on a Windows VM (row GL1).
func TestWorktreeRemoveGoneLockedShortBase(t *testing.T) {
	requireGit(t)
	repo := filepath.Join(resolveTestRoot(t, t.TempDir()), "longrepository1")
	copyFixtureTemplate(t, "worktree-remove", repo, func(t *testing.T, dir string) {
		runGit(t, dir, "init", "-q", "-b", "main")
		runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	})
	holder := filepath.Join(repo, ".claude", "worktrees")
	runGit(t, repo, "worktree", "add", "-q", "-b", "wt1", filepath.Join(holder, "wt1"))
	runGit(t, repo, "worktree", "lock", filepath.Join(holder, "wt1"))
	if err := os.RemoveAll(holder); err != nil {
		t.Fatal(err)
	}
	short := shortPathOf(t, repo)
	wp := filepath.Join(short, ".claude", "worktrees", "wt1")
	raw := removeFrame(t, map[string]any{"baseRepo": short, "worktreePath": wp, "branchName": "wt1"})
	wantRemoveError(t, raw, "refusing to remove worktree: "+wp+" is gone but its registration is "+
		"locked (git worktree lock); unlock it to remove the registration and branch")
	mustExist(t, filepath.Join(repo, ".git", "worktrees", "wt1", "locked"))
	if !gitExitOK(repo, "show-ref", "--verify", "--quiet", "refs/heads/wt1") {
		t.Error("branch wt1 was deleted")
	}
}

// A registered leaf sent through its 8.3 short name is removed with its branch, but
// its entry stays, because the short name does not match the long path git recorded.
// Measured against f6010b97 on a Windows VM (row N83_leaf).
func TestWorktreeRemoveShortNameKeepsEntry(t *testing.T) {
	f := newRmFixture(t)
	leaf := filepath.Join(filepath.Dir(f.wt), "longworktree1")
	runGit(t, f.repo, "worktree", "add", "-q", "-b", "longworktree1", leaf)
	leafUTF16, err := windows.UTF16PtrFromString(leaf)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(leafUTF16, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		t.Skipf("GetShortPathName(%s): n=%d err=%v", leaf, n, err)
	}
	shortLeaf := filepath.Base(windows.UTF16ToString(buf[:n]))
	if shortLeaf == "longworktree1" {
		t.Skip("8.3 short names are disabled on this volume")
	}
	wp := filepath.Join(filepath.Dir(leaf), shortLeaf)
	raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": wp, "branchName": "longworktree1"})
	if raw != removeOK {
		t.Errorf("reply = %s, want %s", raw, removeOK)
	}
	mustBeGone(t, leaf)
	mustExist(t, filepath.Join(f.repo, ".git", "worktrees", "longworktree1"))
	if f.hasBranch("longworktree1") {
		t.Error("branch longworktree1 is still there")
	}
	// The same leaf by its long name drops the entry (row K02).
	if raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"}); raw != removeOK {
		t.Errorf("reply = %s, want %s", raw, removeOK)
	}
	if _, err := os.Lstat(f.entry()); !os.IsNotExist(err) {
		t.Errorf("entry %s is still there (err=%v)", f.entry(), err)
	}
}

// E8, battery row J2 (Windows VM, 89cb6289): baseRepo and worktreePath go through a
// junction to the repository. The folder and its entry are both gone after the
// removal.
func TestWorktreeRemoveThroughJunctionDropsEntry(t *testing.T) {
	f := newRmFixture(t)
	jt := filepath.Join(filepath.Dir(f.repo), "JT")
	makeJunction(t, jt, f.repo)
	wp := filepath.Join(jt, ".claude", "worktrees", filepath.Base(f.wt))
	raw := removeFrame(t, map[string]any{"baseRepo": jt, "worktreePath": wp})
	if raw != removeOK {
		t.Fatalf("reply = %s, want %s", raw, removeOK)
	}
	mustBeGone(t, f.wt)
	if _, err := os.Lstat(f.entry()); !os.IsNotExist(err) {
		t.Errorf("entry %s is still there (err=%v), want it gone (row J2)", f.entry(), err)
	}
}
