package git

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// switchAskAfterAddOn turns on the call about the repository after the add of a
// create, as on Linux and macOS, for the length of the test.
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

// TestCreateAsksAfterAddOnLinuxAndMacOS: on Linux and macOS the call about the
// repository is right after the `worktree add` of a create, in a first and in a
// second create, and no such call comes after the read-tree (b3081b51 and
// c9125eb8, measured on Linux and macOS). Windows makes no such call
// (claustrum's own, not measured).
func TestCreateAsksAfterAddOnLinuxAndMacOS(t *testing.T) {
	if got, want := createAsksAfterAdd, runtime.GOOS != "windows"; got != want {
		t.Fatalf("createAsksAfterAdd = %v, want %v", got, want)
	}
	if !createAsksAfterAdd {
		t.Skip("Windows makes no call about the repository after the add")
	}
	f := newSeededShadowFixture(t)
	setProbeClock(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	for _, name := range []string{"w1", "w2"} {
		leaf := filepath.Join(f.repo, ".claude", "worktrees", name)
		raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": name, "worktreePath": leaf})
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("git.worktree_create %s = %s, want success", name, raw)
		}
		if n := len(probeCalls(calls)); n != 1 {
			t.Errorf("the create of %s made %d calls about the repository, want 1", name, n)
		}
		add := slices.IndexFunc(calls, func(c envCall) bool { return stubMatches(c.argv, "worktree,add") })
		probe := slices.IndexFunc(calls, envCall.isProbe)
		if add < 0 || probe != add+1 {
			t.Errorf("the create of %s: the add is call %d and the call about the repository is call %d, want the next one", name, add, probe)
		}
		readTree := slices.IndexFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "read-tree") })
		if readTree < 0 || slices.ContainsFunc(calls[readTree:], envCall.isProbe) {
			t.Errorf("the create of %s: a call about the repository after the read-tree (call %d), want none", name, readTree)
		}
	}
}

// TestCreateFromACommitAsksBeforeTheContentCheck: after the add of the commit
// id of a sourceBranch, the call about the repository comes after the read-tree
// and before the `cat-file --batch` of the content check. A right answer makes
// 1 call, and it is kept: a git.info after the create makes none. A wrong first answer
// and a right second one succeed with 2 calls. A wrong answer on every call
// starts no batch and fails the create with worktree_verify_repository after 6
// calls: the leaf and the registration go, and the branch stays with the
// disagree text as its cause (c9125eb8, measured on macOS).
func TestCreateFromACommitAsksBeforeTheContentCheck(t *testing.T) {
	for _, row := range []struct {
		name  string
		lie   int // 0 for no wrong answer, 1 for a wrong first answer, -1 for a wrong answer on every call
		calls int
	}{
		{"a right answer", 0, 1},
		{"a wrong first answer", 1, 2},
		{"a wrong answer on every call", -1, 6},
	} {
		t.Run(row.name, func(t *testing.T) {
			switchAskAfterAddOn(t)
			f := newSeededShadowFixture(t)
			setProbeClock(t)
			logs := captureLogBuf(t)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
				t.Fatalf("git.info = %s", raw)
			}
			commit := gitOut(t, f.repo, "rev-parse", "main")
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
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf, "sourceBranch": "main"})
			if n := len(probeCalls(calls)); n != row.calls {
				t.Errorf("the create made %d calls about the repository, want %d", n, row.calls)
			}
			readTree := slices.IndexFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "read-tree") })
			if first := slices.IndexFunc(calls, envCall.isProbe); readTree < 0 || first < readTree {
				t.Errorf("the read-tree is call %d and the first call about the repository is call %d, want it after the read-tree", readTree, first)
			}
			batch := slices.IndexFunc(calls, func(c envCall) bool { return stubMatches(c.argv, "cat-file,--batch") })
			if row.lie != -1 {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("git.worktree_create = %s, want success", raw)
				}
				if batch < 0 || slices.ContainsFunc(calls[batch:], envCall.isProbe) {
					t.Errorf("the batch is call %d, want it after each call about the repository", batch)
				}
				if row.lie == 0 {
					if _, after := f.info(t, f.repo, nil); len(probeCalls(after)) != 0 {
						t.Errorf("a git.info after the create made %d calls about the repository, want 0", len(probeCalls(after)))
					}
				}
				return
			}
			if batch >= 0 {
				t.Errorf("a cat-file --batch ran (call %d) after the wrong answers, want none", batch)
			}
			d := disagreeText(f.repo, filepath.ToSlash(other), f.gitDir)
			want := contentRefusalStart + fmt.Sprintf("Run \"git fsck\" in %q to check the repository: git couldn't read what it stores for commit %s (%s).", f.repo, commit[:12], d) +
				" Removing it could not finish for " + leaf + ": branch w1 still exists; delete it by hand " +
				"(for example: git branch -d w1, which git refuses if that would lose commits) before retrying " +
				"this branch name: could not check whether another branch or remote-tracking ref reaches its commits (" + d + ")."
			if got, code := createErrorField(t, raw); got != want || code != "worktree_verify_repository" {
				t.Errorf("the error = %s (%s)\n    want %s (worktree_verify_repository)", got, code, want)
			}
			if strings.Contains(raw, "branchKept") {
				t.Errorf("git.worktree_create = %s, want no branchKept", raw)
			}
			if line := "[GitHandler] worktree_create: checked 0 files under .claude, .mcp.json, CLAUDE.md (or CLAUDE.local.md) and AGENTS.md against 0 objects of commit " + commit[:12] + " (0 of the commits and folder listings read by separate git runs) in "; !strings.Contains(logs.String(), line) {
				t.Errorf("the log has no line with %q", line)
			}
			if line := "[GitHandler] worktree_create: stopped using the long-running git cat-file for the new worktree's config check: it did not start; the rest is read with a git process per object or per folder depth (slower, same result)"; !strings.Contains(logs.String(), line) {
				t.Errorf("the log has no line %q", line)
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

// TestCreateStopsAtANotARepositoryAnswerAfterTheAdd: a call about the
// repository after the add whose first run writes `fatal: not a git
// repository` with exit 128 fails the create with the branch text and "git
// finds no repository here: " before git's text. The branch step asks again
// and keeps the branch with the same text (c9125eb8, measured on macOS).
func TestCreateStopsAtANotARepositoryAnswerAfterTheAdd(t *testing.T) {
	switchAskAfterAddOn(t)
	f := newSeededShadowFixture(t)
	setProbeClock(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	answerProbe(t, 128, "", "fatal: not a git repository\n")
	answerSecondProbe(t, 0, "")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if n, want := len(probeCalls(calls)), 2*exit128Runs(); n != want {
		t.Errorf("the create made %d calls about the repository, want %d", n, want)
	}
	d := "git finds no repository here: fatal: not a git repository"
	want := `git worktree add failed: branch "w1" could not be resolved to a commit (` + d +
		"); and the undo could not finish for " + leaf + ": branch w1 still exists; delete it by hand " +
		"(for example: git branch -d w1, which git refuses if that would lose commits) before retrying " +
		"this branch name: could not check whether another branch or remote-tracking ref reaches its commits (" + d + ")"
	if got, code := createErrorField(t, raw); got != want || code != "worktree_add_failed" {
		t.Errorf("the error = %s (%s)\n    want %s (worktree_add_failed)", got, code, want)
	}
	if _, err := os.Lstat(leaf); !os.IsNotExist(err) {
		t.Errorf("the leaf: %v, want it gone", err)
	}
	if !branchExists(t, f.repo, "w1") {
		t.Error("the branch w1 went, want it kept")
	}
}
