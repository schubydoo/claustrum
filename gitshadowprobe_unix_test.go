//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// lowerProbeBound lowers the bound of the call about the repository and the
// wait for its pipes.
func lowerProbeBound(t *testing.T, bound, drain time.Duration) {
	t.Helper()
	oldBound, oldDrain := probeCallBound, shadowCallDrain
	probeCallBound, shadowCallDrain = bound, drain
	t.Cleanup(func() { probeCallBound, shadowCallDrain = oldBound, oldDrain })
}

// holdProbe makes the stub hold the call about the repository for d before git
// runs.
func holdProbe(t *testing.T, d time.Duration) {
	t.Helper()
	slowGit(t, "rev-parse,--git-dir", "pre", d, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_COMMON_DIR=/"+shadowProbeName)
}

// TestProbeBound pins both sides of the bound of the call, with a lowered
// bound. A call that the daemon ended gives the text with "30 seconds" in the
// shape of each method, and a remove makes the call two times (cells 37 and 38
// of s3p2b-edges-linux2, Linux VM. Cell 40 of s3p2b-edges-macos, macOS VM). The
// answer is not kept: the next request asks again, and it is normal when the
// call is quick then (cell 37). No worktree is made or removed (cell 38).
func TestProbeBound(t *testing.T) {
	for i, method := range []string{"git.info", "git.status", "git.list_branches", "git.worktree_create", "git.worktree_remove"} {
		t.Run(method, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			lowerProbeBound(t, 300*time.Millisecond, 100*time.Millisecond)
			holdProbe(t, 5*time.Second)
			m := f.fiveRequests(nil)[i]
			start := time.Now()
			raw, calls := f.call(t, m.method, m.params)
			took := time.Since(start)
			text := "git could not check this repository on the remote machine: it did not finish within 30 seconds in \"" + f.repo + "\"; try again"
			if want := probeErrorShapes(t, f, text)[i]; raw != want {
				t.Errorf("reply = %s\n  want %s", raw, want)
			}
			wantCalls := 1
			if method == "git.worktree_remove" {
				wantCalls = 2
			}
			if got := len(probeCalls(calls)); got != wantCalls || len(calls) != wantCalls+1 {
				t.Errorf("%d calls about the repository of %d calls, want %d and the excludes read", got, len(calls), wantCalls)
			}
			if took < time.Duration(wantCalls)*300*time.Millisecond || took > 4*time.Second {
				t.Errorf("the reply came after %v, want the bound of %d calls and not the hold", took, wantCalls)
			}
			if _, err := os.Stat(f.wt); err != nil {
				t.Errorf("the worktree of the fixture: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.repo, ".claude", "worktrees", "w1")); !os.IsNotExist(err) {
				t.Errorf("a worktree was made: %v", err)
			}
			if got := entryNames(t, daemonFolder(t, f.run)); slices.Contains(got, shadowRepoName(f.gitDir)) {
				t.Errorf("the folder of the daemon holds %v, want no folder of the repository", got)
			}
			// The ended call is not kept. A call inside the bound is good.
			lowerProbeBound(t, 20*time.Second, 100*time.Millisecond)
			holdProbe(t, 50*time.Millisecond)
			raw, calls = f.info(t, f.repo, nil)
			if !strings.Contains(raw, `"isRepo":true`) || len(probeCalls(calls)) != 1 {
				t.Errorf("git.info after the ended call = %s with %d calls about the repository, want a normal reply and one call", raw, len(probeCalls(calls)))
			}
		})
	}
}

// TestProbeInsideACreate: the daemon tests the kept answer again before a call
// of the request, so a create that makes the `.claude` folder asks git again
// inside the request. On 5fd08069 that call is the third of the create, after
// `rev-parse --is-inside-work-tree` and before the next listing, and the same
// create again has it as its first call. A create in a repository that has
// both folders makes none (cell 36 of s3p2b-edges-linux2, Linux VM: the rows
// "no .claude folder, with warm-up" and ".claude/worktrees/w0 exists, with
// warm-up").
func TestProbeInsideACreate(t *testing.T) {
	f := newSeededShadowFixture(t)
	runGit(t, f.repo, "worktree", "remove", "--force", f.wt)
	if err := os.RemoveAll(filepath.Join(f.repo, ".claude")); err != nil {
		t.Fatal(err)
	}
	if _, calls := f.info(t, f.repo, nil); len(probeCalls(calls)) != 1 {
		t.Fatalf("the warm-up made %d calls about the repository, want 1", len(probeCalls(calls)))
	}
	create := func(name string) (string, []string) {
		t.Helper()
		leaf := filepath.Join(f.repo, ".claude", "worktrees", name)
		raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": name, "worktreePath": leaf})
		var short []string
		for _, c := range calls {
			switch {
			case c.isProbe():
				short = append(short, "the call about the repository")
			case c.listing():
				short = append(short, "listing")
			default:
				short = append(short, c.argv[len(c.argv)-1])
			}
		}
		return raw, short
	}
	raw, calls := create("w1")
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	if want := []string{"listing", "--is-inside-work-tree", "the call about the repository", "listing"}; len(calls) < 4 || !slices.Equal(calls[:4], want) {
		t.Errorf("the first calls of the create = %q\nwant %q", calls[:min(4, len(calls))], want)
	}
	if n := len(calls) - len(slices.DeleteFunc(slices.Clone(calls), func(s string) bool { return s == "the call about the repository" })); n != 1 {
		t.Errorf("the create made %d calls about the repository, want 1: %q", n, calls)
	}

	raw, calls = create("w1")
	if !strings.Contains(raw, "already exists, and a new worktree is only ever created in a fresh directory") {
		t.Fatalf("the same create again = %s", raw)
	}
	if want := []string{"the call about the repository", "listing", "--is-inside-work-tree"}; !slices.Equal(calls, want) {
		t.Errorf("the calls of the same create again = %q\nwant %q", calls, want)
	}

	raw, calls = create("w2")
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create of w2 = %s", raw)
	}
	if slices.Contains(calls, "the call about the repository") {
		t.Errorf("a create with both folders there made a call about the repository: %q", calls)
	}
}

// A folder `.probe` that the daemon cannot open keeps its config, and the
// request goes on to the call about the repository. Not measured: the rule is
// claustrum's own.
func TestProbeFolderThatCannotBeOpenedKeepsItsConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a folder with mode 0300")
	}
	f := newShadowFixture(t)
	now := setProbeClock(t)
	f.info(t, f.repo, nil)
	probe := filepath.Join(daemonFolder(t, f.run), shadowProbeName)
	if err := os.WriteFile(filepath.Join(probe, "config"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(probe, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(probe, 0o700) })
	*now = now.Add(probeKeep + time.Second)
	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"isRepo":true`) || !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Errorf("git.info = %s, want a normal reply", raw)
	}
	if !slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "--git-dir") }) {
		t.Error("the request made no call about the repository")
	}
	if fi, err := os.Stat(filepath.Join(probe, "config")); err != nil || fi.Size() != 0 {
		t.Errorf("config after the request: %v, %v, want the 0 bytes", fi, err)
	}
}
