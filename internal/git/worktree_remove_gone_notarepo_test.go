package git

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRemoveOfAGoneWorktreeWhereGitFindsNoRepository runs git.worktree_remove
// where git finds no repository in a folder that the trust check accepted. Its
// call about the repository exits 128 with a text of notARepoStderr, and
// the second run exits 0. On Windows b3081b51 answers
// `{"success":true,"branchKept":true}` for a gone worktree where
// `<baseRepo>\.git\config` has 262, 267 or 269 characters (measured on
// Windows). The other lengths from 262 to 269 are claustrum's own. The success
// rows give the length of the fixture as that length. No branch call runs, and
// nothing is deleted. A worktree folder that exists, another text of git, a
// path of another length and the default of Linux and macOS keep the refusal
// (claustrum's own, not measured).
func TestRemoveOfAGoneWorktreeWhereGitFindsNoRepository(t *testing.T) {
	const notARepo = "fatal: not a git repository (or any of the parent directories): .git\n"
	const kept = `{"jsonrpc":"2.0","id":1,"result":{"success":true,"branchKept":true}}`
	for _, row := range []struct {
		name, stderr string
		gone         bool
		// windowsStep sets goneNotARepoKeepsAll. Else it keeps its default.
		windowsStep bool
		// fixtureLength makes the length of the fixture the measured length.
		fixtureLength bool
		success       bool
	}{
		{"a gone worktree", notARepo, true, true, true, true},
		{"a worktree that exists", notARepo, false, true, true, false},
		{"a gone worktree, another text", "fatal: detected dubious ownership in repository at '/x/repo'\n", true, true, true, false},
		{"a gone worktree, a short path", notARepo, true, true, false, false},
		{"a gone worktree, the default of the system", notARepo, true, false, true, runtime.GOOS == "windows"},
		// `<git>` is the git directory of the fixture with forward slashes.
		// b3081b51 gets this text at 267 and 269 characters (measured on Windows).
		{"a gone worktree, error reading", "fatal: error reading '<git>'\n", true, true, true, true},
		{"a gone worktree, error reading another path", "fatal: error reading '/x/repo/.git'\n", true, true, true, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			if row.windowsStep {
				old := goneNotARepoKeepsAll
				goneNotARepoKeepsAll = true
				t.Cleanup(func() { goneNotARepoKeepsAll = old })
			}
			if row.fixtureLength {
				n := len(filepath.Join(f.repo, ".git", "config"))
				old := notARepoConfigLengths
				notARepoConfigLengths = [2]int{n, n}
				t.Cleanup(func() { notARepoConfigLengths = old })
			}
			answerProbe(t, 128, "", strings.ReplaceAll(row.stderr, "<git>", filepath.ToSlash(f.gitDir)))
			answerSecondProbe(t, 0, "")
			leaf := f.wt
			if row.gone {
				leaf = filepath.Join(f.repo, ".claude", "worktrees", "w1")
			}
			raw, calls := f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": leaf, "branchName": "w1"})
			want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonQuoted(t, lockCheckRefusal(leaf)) + `}}`
			if row.success {
				want = kept
			}
			if raw != want {
				t.Errorf("git.worktree_remove = %s\nwant %s", raw, want)
			}
			if row.success {
				for _, c := range calls {
					if !c.isProbe() && !c.excludesRead() {
						t.Errorf("a call that is no call about the repository ran: %s", strings.Join(c.argv, " "))
					}
				}
			}
			if _, err := os.Stat(f.wt); err != nil {
				t.Errorf("the worktree of the fixture: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.gitDir, "worktrees", "wr")); err != nil {
				t.Errorf("the registration of the fixture: %v", err)
			}
		})
	}
}

// TestMeasuredNotARepoLength pins the lengths of `<baseRepo>\.git\config` that
// get success. b3081b51 answers so at 262, 267 and 269 (measured on Windows),
// and the lengths between are claustrum's own. 259 gave another reply, and 280
// failed the start of git (measured).
func TestMeasuredNotARepoLength(t *testing.T) {
	tail := len(filepath.Join("x", ".git", "config")) - 1
	for _, n := range []int{259, 261, 262, 267, 269, 270, 280} {
		repo := strings.Repeat("q", n-tail)
		if got, want := measuredNotARepoLength(repo), n >= 262 && n <= 269; got != want {
			t.Errorf("a config path of %d characters: %v, want %v", n, got, want)
		}
	}
}

// TestNotARepoStderrAtTheMeasuredLengths takes the error output of git and the
// baseRepo of the cells at 267 and 269 characters. b3081b51 answers success for
// a gone worktree there (measured on Windows). The text passes notARepoStderr,
// and the path passes measuredNotARepoLength. A text for another path fails.
func TestNotARepoStderrAtTheMeasuredLengths(t *testing.T) {
	q := strings.Repeat("q", 200)
	for _, row := range []struct {
		n   int
		dir string
	}{
		{267, `C:\r14\out\cellname-267\R\` + q + `\` + strings.Repeat("q", 28)},
		{269, `C:\r14\out\cellname-269\R\` + q + `\` + strings.Repeat("q", 30)},
	} {
		slashed := strings.ReplaceAll(row.dir, `\`, "/") + "/.git"
		stderr := "fatal: error reading '" + slashed + "'\n"
		if !notARepoStderr(stderr, slashed) {
			t.Errorf("%d: the text of git is no such answer, want one", row.n)
		}
		if notARepoStderr("fatal: error reading 'C:/other/.git'\n", slashed) {
			t.Errorf("%d: the text for another path is such an answer, want none", row.n)
		}
		if !measuredNotARepoLength(row.dir) {
			t.Errorf("%d: the length of the path is not measured, want it", row.n)
		}
	}
}
