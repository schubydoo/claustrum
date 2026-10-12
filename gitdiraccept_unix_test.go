//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// linkAt makes a symbolic link at link to target.
func linkAt(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Errorf("the link %s: %v", link, err)
	}
}

// TestStagedAddWithAFolderThatTakesNoEntry: the folder of the daemon takes no
// new entry. On 5fd08069 the create answers `git worktree add failed: cannot
// prepare the repository's worktrees directory: mkdir
// C:\o\w\run\git-shadow\<S>\.s<n>: Access is denied.` (tables W3 and F1 of
// val-s3p2b2-7-windows, Windows VM, with the two privileges off). The staged
// add runs on Windows only. This test calls it on Linux and macOS, where the
// error of the system reads `permission denied`. Without the words of the step
// the text starts with claustrum's own words.
func TestStagedAddWithAFolderThatTakesNoEntry(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the root user makes an entry in a folder of mode 0500")
	}
	f := newRepoFolderFixture(t)
	clearFolderEnv(t)
	u := f.useOf(t, f.repo)
	if err := os.Chmod(u.folder, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(u.folder, 0o700) })
	_, stderr, err := stagedAdd(t, f, u)
	want := regexp.MustCompile(`^cannot prepare the repository's worktrees directory: mkdir ` +
		regexp.QuoteMeta(filepath.Join(u.folder, ".s")) + `[0-9]{1,10}: permission denied$`)
	if err == nil || !want.MatchString(err.Error()) || stderr != "" {
		t.Errorf("the add = %v, %q\nwant %s", err, stderr, want)
	}
}

// createError is the `error` member of the result of a create reply.
func createError(t *testing.T, raw string) string {
	t.Helper()
	var reply struct {
		Result struct {
			Success bool
			Error   string
		}
	}
	if err := json.Unmarshal([]byte(raw), &reply); err != nil || reply.Result.Success {
		t.Fatalf("the reply %s: %v, want a failed create", raw, err)
	}
	return reply.Result.Error
}

// TestCreateWithASwappedGitDir runs git.worktree_create through the server
// with the steps of a held git directory switched on, and puts a link to a
// folder T that is no git directory at `.git` at one step. The texts are those
// of 5fd08069 on a Windows VM (val-s3p2b2-7-windows). On Linux and macOS no
// create runs these steps: the test sets the switch.
//
//   - Before `rev-parse --absolute-git-dir`: the call exits 128, and the create
//     stops with `cannot locate the repository's git directory: exit status
//     128` (cells W1c and X1p-02 to X1p-05). No `worktree add` runs.
//   - After that call: `cannot list the repository's worktree entries: ".git"
//     is a link or a file …` (cell X1p-06).
//   - Before the read-tree call: the failed checkout with the refusal of the
//     config, and a rollback that keeps the registration and the branch and
//     removes the leaf (cell X1p-11).
//
// In each row T keeps its two entries and gets no other. Without the stop
// after the failed call the reply is the error of `worktree add`. Without the
// test after the call the create goes on to that call. Without the test before
// the read-tree call the reply is the error of git, and without the guard of
// the rollback the text has no registration part.
func TestCreateWithASwappedGitDir(t *testing.T) {
	const realDir = `".git" is a link or a file where Claude needs a real directory; put the directory itself in its place`
	link := func(gitDir string) string {
		return "this repository's git config cannot be used: " + quoteOperand(filepath.Join(gitDir, "config")) +
			": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to"
	}
	for _, row := range []struct {
		step string
		want func(gitDir, leaf string) string
		// added is true when the `worktree add` call ran before the swap.
		added bool
	}{
		{step: "gitdir", want: func(string, string) string {
			return "git worktree add failed: cannot locate the repository's git directory: exit status 128"
		}},
		{step: "answered", want: func(string, string) string {
			return "git worktree add failed: cannot list the repository's worktree entries: " + realDir
		}},
		{step: "checkout", added: true, want: func(gitDir, leaf string) string {
			return "git worktree add failed (checkout): " + link(gitDir) + "; and the undo could not finish for " + leaf +
				": the worktree registration and the branch remain; remove them by hand before retrying (" + realDir + ")"
		}},
	} {
		t.Run(row.step, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			setGitDirMustBeReal(t, true)
			target := newSwapTarget(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"settingsControl":"shadow"`) {
				t.Fatalf("git.info = %s", raw)
			}
			swapAt(t, row.step, f.gitDir, target.dir)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			if got, want := createError(t, raw), row.want(f.gitDir, leaf); got != want {
				t.Errorf("the error = %s\n    want %s", got, want)
			}
			if !strings.Contains(raw, `"errorCode":"worktree_add_failed"`) {
				t.Errorf("the reply = %s, want the code worktree_add_failed", raw)
			}
			target.untouched(t)
			if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
				t.Errorf("the leaf: %v, want it gone", err)
			}
			ran := slices.ContainsFunc(calls, func(c envCall) bool {
				return slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add")
			})
			if ran != row.added {
				t.Errorf("a worktree add call ran: %v, want %v", ran, row.added)
			}
			for _, name := range []string{filepath.Join("worktrees", "w1", "HEAD"), filepath.Join("refs", "heads", "w1")} {
				_, err := os.Lstat(filepath.Join(f.gitDir+"-real", name))
				if row.added && err != nil {
					t.Errorf("%s in the real git directory: %v, want it kept", name, err)
				}
				if !row.added && !os.IsNotExist(err) {
					t.Errorf("%s in the real git directory: %v, want none", name, err)
				}
			}
		})
	}
	t.Run("no swap", func(t *testing.T) {
		f := newSeededShadowFixture(t)
		setGitDirMustBeReal(t, true)
		leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
		raw, _ := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
		if !strings.Contains(raw, `"success":true`) {
			t.Errorf("git.worktree_create = %s, want a good create", raw)
		}
	})
}
