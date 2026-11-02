package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// homeEnvVar is the variable os.UserHomeDir actually reads on this platform.
// Setting the wrong one leaves the REAL home in place, which would make every
// assertion below vacuous — and, in the unfixed tree these tests are written to
// fail against, would point a live os.RemoveAll at the developer's home
// directory. That is the exact accident this file exists to prevent, so the
// switch is not a portability nicety.
func homeEnvVar() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

// git.worktree_remove must refuse a worktreePath that is (or contains) the home
// directory.
//
// This is the SECOND caller-supplied path handed to a recursive delete, found while
// fixing the first: the daemon deletes worktreePath itself, and `~` expands there
// too. (At the time a failed `git worktree remove --force` led to that delete.
// Today the remove runs no such git call.)
//
// Unlike the extract_tar wipe there is no seam on that delete, so safety here
// comes from the home being a t.TempDir(): against the unfixed tree this test
// fails by deleting a temporary directory, never a real one. KEEP.txt is the
// assertion, because it is the thing that would actually be gone. A TABLE,
// not a single row, and the ancestor row is the load-bearing one: on this
// method wipesHomeDir is the ONLY gate, where filesExtractTar still has IsAbs
// and isFilesystemRoot behind it. If the predicate ever narrows to "the home
// directory itself", extract_tar's ancestor case is caught by its root check
// and this one is not. Raised in review on #231.
func TestWorktreeRemoveRefusesHomeDir(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home", "someone")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)
	s := newTestServer(t)

	for _, tc := range []struct {
		name, worktreePath string
	}{
		// The bare tilde the fuzzer sent — also the end-to-end proof, since
		// nothing in gitWorktreeRemove mentions "~".
		{"bare tilde", "~"},
		{"the home directory spelled out", home},
		{"the parent of home", filepath.Dir(home)},
		// Relative, resolved against the daemon's working directory — the spelling
		// the guard missed entirely before filepath.Abs was added.
		//
		// ".." is the one that actually destroys: os.RemoveAll special-cases a
		// trailing "." and returns EINVAL, so "." could not have deleted anything
		// even unguarded, while ".." from a daemon sitting in the home directory
		// resolves to home's PARENT and takes home with it. Both rows are kept —
		// "." pins that the refusal is the guard's doing and not an os.RemoveAll
		// accident.
		{"dot, with the daemon's cwd in home", "."},
		{"dotdot, with the daemon's cwd in home", ".."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh marker per row: each row must independently prove nothing was
			// deleted, and the previous row must not be able to satisfy it.
			keep := filepath.Join(home, "KEEP.txt")
			if err := os.WriteFile(keep, []byte("must survive"), 0o600); err != nil {
				t.Fatal(err)
			}
			if !filepath.IsAbs(tc.worktreePath) && tc.worktreePath != "~" {
				t.Chdir(home)
			}
			// baseRepo is a real directory but not a repository; the home path is
			// outside it, so 7d193f89's location containment refuses it — with the
			// reference's own "refusing to remove worktree:" wording — before git,
			// the delete, or the wipesHomeDir guard is ever reached.
			// Containment is what protects home here now; the guard remains as
			// defense-in-depth (and as the primary guard for files.extract_tar).
			got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
				map[string]any{"baseRepo": t.TempDir(), "worktreePath": tc.worktreePath}))

			if _, err := os.Stat(keep); err != nil {
				t.Errorf("worktreePath %q deleted the home directory: %v", tc.worktreePath, err)
			}
			if !strings.Contains(got, "refusing to remove worktree:") {
				t.Errorf("worktree_remove %q = %s, want a containment refusal", tc.worktreePath, got)
			}
			if strings.Contains(got, `"success":true`) {
				t.Errorf("worktree_remove %q = %s, want success:false — nothing was removed", tc.worktreePath, got)
			}
		})
	}
}

// An OMITTED worktreePath is refused by the reference's own non-directory check,
// NOT by claustrum's wipesHomeDir guard — and this row pins that ordering.
//
// 7d193f89 added a worktree-location check to git.worktree_remove that fails an
// empty worktreePath as `failed to remove worktree: "" does not name a directory`
// before anything is deleted. claustrum matches that frame, and matches it
// FIRST: the empty case returns before the containment check, the wipesHomeDir
// guard, and git. So even with the daemon's cwd and HOME set to the same
// directory — the cwd that once made filepath.Abs("") resolve to home and the old
// guard refuse — the reply is the reference's non-directory message, never a
// "home directory" one. That the home-directory wording is absent is what proves
// the empty branch wins the race with the guard. Ordering issue raised on #232.
func TestWorktreeRemoveEmptyPathFailsAsNonDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home", "someone")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)
	// The cwd is what made this bite once: an empty path resolved to it.
	t.Chdir(home)

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
		map[string]any{"baseRepo": t.TempDir(), "branchName": "b1"}))

	if strings.Contains(got, "home directory") {
		t.Errorf("worktree_remove with no worktreePath = %s, want the reference's "+
			"non-directory refusal, not the home-directory guard", got)
	}
	if !strings.Contains(got, `does not name a directory`) ||
		!strings.Contains(got, `"success":false`) {
		t.Errorf("worktree_remove with no worktreePath = %s, want the bare "+
			`failed to remove worktree: "" does not name a directory`, got)
	}
}
