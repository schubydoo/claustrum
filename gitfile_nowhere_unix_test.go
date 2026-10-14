//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The three call sites of gitFileText in git.worktree_remove, with a stub that fails
// every listing with the text of git 2.56 for a `.git` file that names no valid
// repository. So no git 2.56 is needed. The frames are those of 5fd08069 in cells
// WR13, GF and N04 of round tier0-27 (Linux VM). The subfolder case is not measured.
// Mutations: invert the gitFileText test at any of the three sites, or name dir in
// place of the folder that holds `.git`.
func TestWorktreeRemoveGitFileNowhereStub(t *testing.T) {
	f := newWRFixture(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	g := filepath.Join(f.base, "G")
	wrWrite(t, filepath.Join(g, ".git"), "gitdir: "+filepath.Join(f.base, "nowhere")+"\n")
	if err := os.MkdirAll(filepath.Join(g, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	installGitSlowStub(t, realGit)
	stub := func(holder string) {
		stubListing(t, 128, "fatal: gitfile does not point to a valid repository: "+filepath.Join(holder, ".git")+"\n")
	}
	check := `git could not check this repository on the remote machine: at "` + g +
		`": fatal: gitfile does not point to a valid repository: ` + filepath.Join(g, ".git")

	t.Run("WR13", func(t *testing.T) {
		if err := os.RemoveAll(f.eTW); err != nil {
			t.Fatal(err)
		}
		stub(f.TW)
		if got, want := f.remove(t, f.TW, f.e1, "e1"), refusalFrame(t, wtUnknown+"exit status 128"); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		f.kept(t, f.e1, "e1")
	})
	for _, base := range []string{g, filepath.Join(g, "sub")} {
		t.Run("GF "+filepath.Base(base), func(t *testing.T) {
			stub(g)
			if got, want := f.remove(t, base, f.p0, ""), refusalFrame(t, wtUnknown+check); got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			if _, err := os.Stat(filepath.Join(f.p0, "keep.txt")); err != nil {
				t.Errorf("the plain folder was touched: %v", err)
			}
		})
	}
	t.Run("N04 without worktreeRoot", func(t *testing.T) {
		stub(g)
		wp := filepath.Join(g, ".claude", "worktrees", "w1")
		s := newTestServer(t)
		got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove", map[string]any{
			"baseRepo": g, "worktreePath": wp, "branchName": "w1"}))
		want := refusalFrame(t, "failed to remove worktree: could not check whether "+wp+" is locked: "+check)
		if got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
}
