package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// switchAskAfterAddOn turns on the call about the repository after the add of a
// create, as on macOS, for the length of the test.
func switchAskAfterAddOn(t *testing.T) {
	t.Helper()
	old := createAsksAfterAdd
	createAsksAfterAdd = true
	t.Cleanup(func() { createAsksAfterAdd = old })
}

// TestCreateAsksAboutTheRepositoryAfterTheAdd: on macOS a create asks git about
// the repository after its `worktree add`, also right after a git.info. A right
// answer goes on with 1 call. A wrong first answer and a right second one
// succeed with 2 calls. A wrong answer on every call fails the create after 4
// calls: the leaf and the registration go, and the branch stays with the
// disagree text as its cause (c9125eb8, measured on macOS).
func TestCreateAsksAboutTheRepositoryAfterTheAdd(t *testing.T) {
	for _, row := range []struct {
		name string
		// lie is 0 for no wrong answer, 1 for a wrong first answer and -1 for
		// a wrong answer on every call.
		lie   int
		calls int
	}{
		{"a right answer", 0, 1},
		{"a wrong first answer", 1, 2},
		{"a wrong answer on every call", -1, 4},
	} {
		t.Run(row.name, func(t *testing.T) {
			switchAskAfterAddOn(t)
			f := newSeededShadowFixture(t)
			setProbeClock(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
				t.Fatalf("git.info = %s", raw)
			}
			other := filepath.Join(t.TempDir(), "decoy", ".git")
			if err := os.MkdirAll(other, 0o755); err != nil {
				t.Fatal(err)
			}
			if row.lie != 0 {
				answerProbe(t, 0, filepath.ToSlash(other)+`\n`, "")
			}
			if row.lie == 1 {
				t.Setenv("CLAUSTRUM_GITSTUB_NTHFILE", filepath.Join(t.TempDir(), "nth"))
				t.Setenv("CLAUSTRUM_GITSTUB_NTH", "1")
			}
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			probes := probeCalls(calls)
			if len(probes) != row.calls {
				t.Errorf("the create made %d calls about the repository, want %d", len(probes), row.calls)
			}
			// Each call comes after the add.
			add := slices.IndexFunc(calls, func(c envCall) bool { return stubMatches(c.argv, "worktree,add") })
			if first := slices.IndexFunc(calls, envCall.isProbe); add < 0 || first < add {
				t.Errorf("the add is call %d and the first call about the repository is call %d, want it after the add", add, first)
			}
			if row.lie != -1 {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("git.worktree_create = %s, want success", raw)
				}
				return
			}
			d := disagreeText(f.repo, filepath.ToSlash(other), f.gitDir)
			want := `git worktree add failed: branch "w1" could not be resolved to a commit (` + d +
				"); and the undo could not finish for " + leaf + ": branch w1 still exists; delete it by hand " +
				"(for example: git branch -d w1, which git refuses if that would lose commits) before retrying " +
				"this branch name: could not check whether another branch or remote-tracking ref reaches its commits (" + d + ")"
			if got, code := createErrorField(t, raw); got != want || code != "worktree_add_failed" {
				t.Errorf("the error = %s (%s)\n    want %s (worktree_add_failed)", got, code, want)
			}
			if strings.Contains(raw, "branchKept") {
				t.Errorf("git.worktree_create = %s, want no branchKept", raw)
			}
			if slices.ContainsFunc(calls, func(c envCall) bool {
				return slices.Contains(c.argv, "--verify") || slices.Contains(c.argv, "for-each-ref")
			}) {
				t.Errorf("a call of the commit or of the branch check ran after a wrong answer")
			}
			if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
				t.Errorf("the leaf: %v, want it gone", err)
			}
			if _, err := os.Lstat(filepath.Join(f.gitDir, "worktrees", "w1")); !os.IsNotExist(err) {
				t.Errorf("the registration: %v, want it gone", err)
			}
			if !branchExists(t, f.repo, "w1") {
				t.Error("the branch w1 went, want it kept")
			}
		})
	}
}
