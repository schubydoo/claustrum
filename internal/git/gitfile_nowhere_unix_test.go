//go:build unix

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
)

// The three call sites of gitFileText in git.worktree_remove, with a stub that fails
// every listing with the text of git 2.56 for a `.git` file that names no valid
// repository. So no git 2.56 is needed. The frames of G are those of 5fd08069
// (measured on Linux). The subfolder case is not measured. With "shadowDirOff":true
// the listing refusal takes the place of that text (measured on Linux). A request
// without worktreeRoot for a gone worktree gets success with the branch kept on b3081b51, with and
// without the member (measured on Linux). The branch step does not run,
// and G keeps its entries. Mutations: invert the gitFileText test at any of the three
// sites, name dir in place of the folder that holds `.git`, or ignore the member at
// the site of G.
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

	t.Run("linked base with its entry removed", func(t *testing.T) {
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
		t.Run("G "+filepath.Base(base), func(t *testing.T) {
			stub(g)
			if got, want := f.remove(t, base, f.p0, ""), refusalFrame(t, wtUnknown+check); got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			if _, err := os.Stat(filepath.Join(f.p0, "keep.txt")); err != nil {
				t.Errorf("the plain folder was touched: %v", err)
			}
		})
	}
	kept := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
	// goneKept tests the disk and the branch step after a request for a gone worktree.
	goneKept := func(t *testing.T, steps *atomic.Int32) {
		t.Helper()
		if n := steps.Load(); n != 0 {
			t.Errorf("the branch step ran %d times, want 0", n)
		}
		if got := entryNames(t, g); !slices.Equal(got, []string{".git", "sub"}) {
			t.Errorf("G holds %q, want .git and sub", got)
		}
	}
	t.Run("gone worktree without worktreeRoot", func(t *testing.T) {
		stub(g)
		steps := branchStepRuns(t)
		wp := filepath.Join(g, ".claude", "worktrees", "w1")
		d := newTestGitDeps()
		got := gitDispatch(t, d, rpcLine(t, "git.worktree_remove", map[string]any{
			"baseRepo": g, "worktreePath": wp, "branchName": "w1"}))
		if got != kept {
			t.Errorf("got  %s\nwant %s", got, kept)
		}
		goneKept(t, steps)
	})
	hooks := "config-defined hooks could not be pinned off; git not run: listing the configuration in force: " +
		"exit status 128: fatal: gitfile does not point to a valid repository: " + filepath.Join(g, ".git")
	t.Run("G shadowDirOff", func(t *testing.T) {
		stub(g)
		d, _ := newShadowDeps(t)
		got := gitDispatch(t, d, rpcLine(t, "git.worktree_remove", map[string]any{
			"baseRepo": g, "worktreePath": f.p0, "branchName": "", "worktreeRoot": f.R, "shadowDirOff": true}))
		if want := refusalFrame(t, wtUnknown+hooks); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		if _, err := os.Stat(filepath.Join(f.p0, "keep.txt")); err != nil {
			t.Errorf("the plain folder was touched: %v", err)
		}
	})
	t.Run("gone worktree shadowDirOff", func(t *testing.T) {
		stub(g)
		steps := branchStepRuns(t)
		wp := filepath.Join(g, ".claude", "worktrees", "w1")
		d, _ := newShadowDeps(t)
		got := gitDispatch(t, d, rpcLine(t, "git.worktree_remove", map[string]any{
			"baseRepo": g, "worktreePath": wp, "branchName": "w1", "shadowDirOff": true}))
		if got != kept {
			t.Errorf("got  %s\nwant %s", got, kept)
		}
		goneKept(t, steps)
	})
}
