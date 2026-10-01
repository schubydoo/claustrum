//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The stray-commondir rows that need a FIFO, a symlink or a mode bit. They run on
// Linux and macOS only.

// A FIFO, an absolute symlink that leaves the git directory, and a file
// the daemon cannot read are refused with their reasons and the stray tail (rows T09,
// T07a and T08). Mutation: reuse the tail of the entry text. A dangling symlink that is
// absolute, or that leaves the git directory, is refused the same way (rows T07d and
// T07e on a Linux VM). Mutation: count every dangling symlink as no commondir.
func TestStrayCommondirNotAPlainFileUnix(t *testing.T) {
	t.Run("FIFO", func(t *testing.T) {
		r := newTrustRepo(t)
		cd := filepath.Join(r.T, ".git", "commondir")
		mkfifo(t, cd)
		rep := dispatchWithin(t, cd, "git.info", map[string]any{"path": r.T})
		wantRPCError(t, "info(T)", rep, wantTP(cd, "commondir is not a regular file"))
	})
	t.Run("absolute symlink out", func(t *testing.T) {
		r := newTrustRepo(t)
		out := filepath.Join(r.base, "out.txt")
		writeFile(t, out, ".", 0o644)
		cd := filepath.Join(r.T, ".git", "commondir")
		symlink(t, out, cd)
		wantRPCError(t, "info(T)", info(t, r.T), wantTP(cd, "openat commondir: path escapes from parent"))
	})
	for _, tc := range []struct{ name, target string }{
		{"T07d absolute dangling symlink", "/nonexistent-claustrum-t07d"},
		{"T07e relative dangling symlink out", "../nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			cd := filepath.Join(r.T, ".git", "commondir")
			symlink(t, tc.target, cd)
			want := wantTP(cd, "openat commondir: path escapes from parent")
			wantRPCError(t, "info(T)", info(t, r.T), want)
			wantCreateRefused(t, r.T, r.T, want)
		})
	}
	t.Run("mode 000", func(t *testing.T) {
		skipIfRoot(t)
		r := newTrustRepo(t)
		cd := filepath.Join(r.T, ".git", "commondir")
		writeFile(t, cd, ".", 0o644)
		if err := os.Chmod(cd, 0); err != nil {
			t.Fatal(err)
		}
		wantRPCError(t, "info(T)", info(t, r.T), wantTP(cd, "openat commondir: permission denied"))
	})
}

// A relative symlink that stays in the git directory is followed. Its
// target holds ".", so the repository is served (row T07b). Mutation: refuse every
// symlink.
func TestStrayCommondirRelativeSymlink(t *testing.T) {
	r := newTrustRepo(t)
	gitDir := filepath.Join(r.T, ".git")
	writeFile(t, filepath.Join(gitDir, "cd.txt"), ".", 0o644)
	symlink(t, "cd.txt", filepath.Join(gitDir, "commondir"))
	wantResultPrefix(t, "info(T)", info(t, r.T), `{"isRepo":true,"repo":"T"`)
	c, wt := create(t, r.T)
	wantResultPrefix(t, "create(T)", c, `{"success":true`)
	wantResultPrefix(t, "remove(T)", remove(t, r.T, wt, "w1"), `{"success":true`)
}

// A dangling relative symlink that stays in the git directory counts as no commondir.
// The git directory is trusted
// and pinned, and git itself then fails to read the file. Each answer is what the
// existing code makes of git's failure (row T07c): info without branch, list_branches
// "exit status 128", status the bare shape, create the failed add, and remove keeps
// the branch. The text of the failed add is git's own, so the test pins only that it
// names commondir. Mutation: treat the dangling symlink as not a small plain file.
func TestStrayCommondirDanglingSymlink(t *testing.T) {
	r := newTrustRepo(t)
	symlink(t, "nothing-here", filepath.Join(r.T, ".git", "commondir"))
	wantResult(t, "info(T)", info(t, r.T),
		`{"isRepo":true,"repo":"T","root":"`+jsonEscape(t, r.T)+`","repoSlug":"","defaultBranch":""}`)
	wantRPCError(t, "list_branches(T)", listBranches(t, r.T), "exit status 128")
	// The measured request is status with path = baseRepo = T.
	wantResult(t, "status(T,T)", status(t, r.T, r.T), notRepoStatus)
	c, wt := create(t, r.T)
	var res worktreeResult
	if err := json.Unmarshal(c.Result, &res); err != nil || res.Success || res.ErrorCode != "worktree_add_failed" ||
		!strings.HasPrefix(res.Error, "git worktree add failed: ") || !strings.Contains(res.Error, "commondir") {
		t.Errorf("create(T) = %s, want git's failure to read commondir", c.Result)
	}
	wantResult(t, "remove(T)", remove(t, r.T, wt, "w1"), `{"success":true,"branchKept":true}`)
}

// Rows DG3-g and DG3x-g (89cb6289, Linux VM). The request is a remove without
// worktreeRoot. The repository has a good HEAD and a dangling relative commondir
// symlink that stays in `.git`. The answer is success with branchKept. The worktree folder and its entry are
// deleted, and the branch stays, because git cannot list the refs. In row DG3x-g the
// folder is gone before the request, and the entry is deleted. These are pins of rows
// that were equal already. No fix stands behind them. The fixture lives in the test's
// temporary directory only.
func TestStrayCommondirDanglingSymlinkRemoveInRepository(t *testing.T) {
	for _, tc := range []struct {
		name     string
		leafGone bool
	}{
		{"DG3-g_worktree_present", false},
		{"DG3x-g_worktree_gone", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			entry := filepath.Join(r.T, ".git", "worktrees", "s0")
			branch := filepath.Join(r.T, ".git", "refs", "heads", "s0")
			if tc.leafGone {
				if err := os.RemoveAll(r.SW); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range []string{entry, branch} {
				if !exists(p) {
					t.Fatalf("fixture: %s is missing", p)
				}
			}
			symlink(t, "nothing-here", filepath.Join(r.T, ".git", "commondir"))
			wantResult(t, "remove(T)", remove(t, r.T, r.SW, "s0"), `{"success":true,"branchKept":true}`)
			for _, p := range []string{r.SW, entry} {
				if exists(p) {
					t.Errorf("%s is still there", p)
				}
			}
			if !exists(branch) {
				t.Errorf("the branch file %s is gone", branch)
			}
		})
	}
}
