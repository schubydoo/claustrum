package git

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// manyRegistrations adds n real folders `e<i>` to `<gitDir>/worktrees`.
func manyRegistrations(t *testing.T, gitDir string, n int) {
	t.Helper()
	for i := range n {
		if err := os.Mkdir(filepath.Join(gitDir, "worktrees", "e"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestStagedAddRegistrationLimit: a repository with more than 4096
// registrations is refused with registrationLimitText, no add runs, and
// `.s<n>` is gone after. 4096 pass. 4c111e86 refuses 4097 old entries with no
// add and adds with 4096 (measured on Linux).
func TestStagedAddRegistrationLimit(t *testing.T) {
	for _, tc := range []struct {
		total   int
		refused bool
	}{{registrationLimit, false}, {registrationLimit + 1, true}} {
		f := newRepoFolderFixture(t)
		clearFolderEnv(t)
		u := f.useOf(t, f.repo)
		manyRegistrations(t, f.gitDir, tc.total-1) // the fixture has wr
		_, stderr, err := stagedAdd(t, f, u)
		var own *addOwnFiles
		refused := errors.As(err, &own) && own.text == registrationLimitText
		if refused != tc.refused || (tc.refused && stderr != "") {
			t.Errorf("%d registrations: the add = %v, %q, want refused %v", tc.total, err, stderr, tc.refused)
		}
		if !tc.refused {
			continue
		}
		if got := addFolders(t, u.folder); len(got) != 0 {
			t.Errorf("%d registrations: the folder of the daemon holds %q, want no .s<n>", tc.total, got)
		}
		if _, err := os.Lstat(filepath.Join(f.gitDir, "refs", "heads", "w1")); !os.IsNotExist(err) {
			t.Errorf("%d registrations: the branch w1: %v, want no add", tc.total, err)
		}
	}
}

// TestCreateOverTheRegistrationLimit: the frame of a create with 4097 old
// entries, as 4c111e86 answers it on Linux (measured). The leaf goes.
func TestCreateOverTheRegistrationLimit(t *testing.T) {
	f := newSeededShadowFixture(t)
	manyRegistrations(t, filepath.Join(f.repo, ".git"), registrationLimit)
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"cannot add a worktree: the repository has more than 4096 worktrees; remove some (git worktree prune, git worktree remove)","errorCode":"worktree_add_failed"}}`
	if raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
}
