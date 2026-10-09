package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The branch step of git.worktree_remove and of the git.worktree_create rollbacks,
// measured side by side against f6010b97 and 89cb6289 on Linux, macOS and Windows
// VMs. Each test names its rows. A kept row checks that the branch survives with its
// commit. A deleted row checks that it is gone.

// realGitPath returns the git on PATH before a test puts the stub there.
func realGitPath(t *testing.T) string {
	t.Helper()
	requireGit(t)
	p, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// refAt returns the object of refs/heads/<name> in repo, or "" when the branch is
// gone. It asks the real git, so a stub rule never touches the answer.
func refAt(t *testing.T, realGit, repo, name string) string {
	t.Helper()
	cmd := exec.Command(realGit, "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// addPackedHeads writes a packed-refs file into gitDir that holds refs/heads/<name>
// at sha for each name. git sorts a packed-refs file without a header itself.
func addPackedHeads(t *testing.T, gitDir, sha string, names []string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(gitDir, "reftable")); err == nil {
		t.Skip("the repository uses reftable, so no packed-refs file")
	}
	var b strings.Builder
	for _, n := range names {
		b.WriteString(sha + " refs/heads/" + n + "\n")
	}
	p := filepath.Join(gitDir, "packed-refs")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// numbered returns n names prefix00000, prefix00001, ...
func numbered(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%05d", prefix, i)
	}
	return out
}

// stubNever is a stub sleep that no test waits out. A bound under test stops the
// call long before it ends, and stoppedCeiling shows that it did.
const (
	stubNever      = 30 * time.Second
	stoppedCeiling = 15 * time.Second
	// d5BranchDeadline is the D5 deadline of the case "D5 stops for-each-ref".
	d5BranchDeadline = 10 * time.Second
	// d5StoppedCeiling is above that deadline and under stubNever, so a call that no
	// deadline stopped still fails the case.
	d5StoppedCeiling = d5BranchDeadline + 15*time.Second
)

// shortBounds shrinks the three bounds of the branch step for one test.
func shortBounds(t *testing.T, check, stop, kill time.Duration) {
	t.Helper()
	oldCheck, oldStop, oldKill := branchCheckBound, updateRefStop, updateRefKillAfter
	t.Cleanup(func() { branchCheckBound, updateRefStop, updateRefKillAfter = oldCheck, oldStop, oldKill })
	branchCheckBound, updateRefStop, updateRefKillAfter = check, stop, kill
}

// stubRule2 sets the second rule of the git stub (runGitSlow).
func stubRule2(t *testing.T, match, mode string, d time.Duration, exit, action, leaf string) {
	t.Helper()
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", match)
	t.Setenv("CLAUSTRUM_GITSTUB_MODE2", mode)
	t.Setenv("CLAUSTRUM_GITSTUB_MS2", fmt.Sprint(d.Milliseconds()))
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", exit)
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION2", action)
	t.Setenv("CLAUSTRUM_GITSTUB_LEAF2", leaf)
}

// killedText is the process error of a call killed at a bound: SIGKILL on Linux and
// macOS, and Go's Process.Kill on Windows, which ends the process with exit code 1.
func killedText() string {
	if runtime.GOOS == "windows" {
		return "exit status 1"
	}
	return "signal: killed"
}

// stoppedText is the process error of an update-ref stopped at updateRefStop:
// SIGTERM on Linux and macOS (row B2-09e), a kill on Windows (row R23a).
func stoppedText() string {
	if runtime.GOOS == "windows" {
		return "exit status 1"
	}
	return "signal: terminated"
}

func TestBranchStdin(t *testing.T) {
	rec := func(name, symref string) branchRecord { return branchRecord{object: "c", name: name, symref: symref} }
	for _, tc := range []struct {
		name   string
		recs   []branchRecord
		target string
		want   string
	}{
		// R01: main is the only other branch.
		{"R01", []branchRecord{rec("refs/heads/main", ""), rec("refs/heads/s", "")}, "refs/heads/s", "^refs/heads/main\n"},
		// R04: every other branch, in listing order.
		{"R04", []branchRecord{rec("refs/heads/k", ""), rec("refs/heads/main", ""), rec("refs/heads/s", "")}, "refs/heads/s",
			"^refs/heads/k\n^refs/heads/main\n"},
		// R08: a symref to the target is left out.
		{"R08", []branchRecord{rec("refs/heads/alias", "refs/heads/s"), rec("refs/heads/main", ""), rec("refs/heads/s", "")},
			"refs/heads/s", "^refs/heads/main\n"},
		// B2-05: the target is the symref, and the branch it names is a normal record.
		{"B2-05", []branchRecord{rec("refs/heads/alias", "refs/heads/s"), rec("refs/heads/main", ""), rec("refs/heads/s", "")},
			"refs/heads/alias", "^refs/heads/main\n^refs/heads/s\n"},
		// B2-04a: the target is the only branch.
		{"B2-04a", []branchRecord{rec("refs/heads/s", "")}, "refs/heads/s", ""},
	} {
		if got := branchStdin(tc.recs, tc.target); got != tc.want {
			t.Errorf("%s: stdin = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A skipped name gets its own rollback text (rows X1 and X2 for "+x"). A name that
// starts with "-" gets the same text: claustrum's choice (not measured). No branch
// (attach mode) gets no text.
func TestRollbackTextOfSkippedName(t *testing.T) {
	skipped := branchStepResult{kind: branchNotRun}
	for _, b := range []string{"+x", "-x"} {
		want := "branch " + b + " left in place (its name is unsafe to pass to update-ref); delete it by hand"
		if got := rollbackBranchText("", b, skipped, false); got != want {
			t.Errorf("%s: text = %q, want %q", b, got, want)
		}
	}
	if got := rollbackBranchText("", "", skipped, false); got != "" {
		t.Errorf("attach mode: text = %q, want none", got)
	}
}

func TestParseBranchRecords(t *testing.T) {
	recs, ok := parseBranchRecords("c1\x00refs/heads/main\x00\nc2\x00refs/heads/alias\x00refs/heads/s\n")
	if !ok || len(recs) != 2 || recs[0] != (branchRecord{"c1", "refs/heads/main", ""}) ||
		recs[1] != (branchRecord{"c2", "refs/heads/alias", "refs/heads/s"}) {
		t.Errorf("parse = %q %v", recs, ok)
	}
	if recs, ok := parseBranchRecords(""); !ok || len(recs) != 0 {
		t.Errorf("empty listing = %q %v, want no records", recs, ok)
	}
	if _, ok := parseBranchRecords("c1 refs/heads/main\n"); ok {
		t.Error("a record without NUL fields parsed")
	}
}

// TestWorktreeRemoveBranchStep pins the branch step of git.worktree_remove. f is a
// repository on main at c1 with worktree wt1 on branch wt1 at c1. The worktree
// directory and its entry go first in every row.
func TestWorktreeRemoveBranchStep(t *testing.T) {
	realGit := realGitPath(t)
	// unique makes a commit c2 on wt1 that no other ref reaches.
	unique := func(t *testing.T, f rmFixture) string {
		runGit(t, f.wt, "commit", "-q", "--allow-empty", "-m", "c2")
		return refAt(t, realGit, f.repo, "wt1")
	}
	gitDir := func(f rmFixture) string { return filepath.Join(f.repo, ".git") }
	for _, tc := range []struct {
		name   string
		branch string
		setup  func(t *testing.T, f rmFixture)
		kept   bool
		// check runs after the reply. Else a kept row wants wt1 at its commit, and a
		// deleted row wants wt1 gone.
		check func(t *testing.T, f rmFixture, before string)
	}{
		{name: "R01 main at the tip"},
		{name: "R02 a commit no other ref reaches", setup: func(t *testing.T, f rmFixture) { unique(t, f) }, kept: true},
		{name: "R03 a remote-tracking ref at the tip", setup: func(t *testing.T, f rmFixture) {
			runGit(t, f.repo, "update-ref", "refs/remotes/origin/wt1", unique(t, f))
		}},
		{name: "R04 another branch at the tip", setup: func(t *testing.T, f rmFixture) {
			runGit(t, f.repo, "branch", "k", unique(t, f))
		}},
		{name: "R04b another branch past the tip", setup: func(t *testing.T, f rmFixture) {
			unique(t, f)
			runGit(t, f.wt, "commit", "-q", "--allow-empty", "-m", "c3")
			runGit(t, f.wt, "branch", "k")
			runGit(t, f.wt, "reset", "-q", "--soft", "HEAD~1")
		}},
		{name: "R06 only a tag", setup: func(t *testing.T, f rmFixture) {
			runGit(t, f.repo, "tag", "v", unique(t, f))
		}, kept: true},
		{name: "R07 a detached HEAD at the tip", setup: func(t *testing.T, f rmFixture) {
			runGit(t, f.repo, "checkout", "-q", "--detach", unique(t, f))
		}, kept: true},
		{name: "R08 a symref to the branch", setup: func(t *testing.T, f rmFixture) {
			unique(t, f)
			runGit(t, f.repo, "symbolic-ref", "refs/heads/alias", "refs/heads/wt1")
		}, kept: true},
		{name: "B2-05 the symref itself", branch: "alias", setup: func(t *testing.T, f rmFixture) {
			unique(t, f)
			runGit(t, f.repo, "symbolic-ref", "refs/heads/alias", "refs/heads/wt1")
		}, check: func(t *testing.T, f rmFixture, before string) {
			if refAt(t, realGit, f.repo, "alias") != "" {
				t.Error("alias is still there, want the symref deleted")
			}
			if got := refAt(t, realGit, f.repo, "wt1"); got != before {
				t.Errorf("wt1 = %q, want it kept at %s", got, before)
			}
		}},
		{name: "R11 a missing branch", branch: "nosuch", check: func(t *testing.T, f rmFixture, before string) {
			if got := refAt(t, realGit, f.repo, "wt1"); got != before {
				t.Errorf("wt1 = %q, want it untouched at %s", got, before)
			}
		}},
		{name: "R14 a lock on the branch", setup: func(t *testing.T, f rmFixture) {
			writeFile(t, filepath.Join(gitDir(f), "refs", "heads", "wt1.lock"), "", 0o644)
		}, kept: true, check: func(t *testing.T, f rmFixture, before string) {
			if got := refAt(t, realGit, f.repo, "wt1"); got != before {
				t.Errorf("wt1 = %q, want it kept at %s", got, before)
			}
			mustExist(t, filepath.Join(gitDir(f), "refs", "heads", "wt1.lock"))
		}},
		{name: "R16 a packed twin in another letter case", setup: func(t *testing.T, f rmFixture) {
			addPackedHeads(t, gitDir(f), refAt(t, realGit, f.repo, "wt1"), []string{"WT1"})
		}, kept: true},
		{name: "R16b the request names the twin", branch: "WT1", kept: true, check: func(t *testing.T, f rmFixture, before string) {
			if got := refAt(t, realGit, f.repo, "wt1"); got != before {
				t.Errorf("wt1 = %q, want it kept at %s", got, before)
			}
		}},
		{name: "R15a 10001 heads", setup: func(t *testing.T, f rmFixture) {
			addPackedHeads(t, gitDir(f), refAt(t, realGit, f.repo, "wt1"), numbered("zz", branchListLimit-2))
		}, kept: true},
		{name: "R15c 10002 heads, wt1 not listed", setup: func(t *testing.T, f rmFixture) {
			addPackedHeads(t, gitDir(f), refAt(t, realGit, f.repo, "wt1"), numbered("b", branchListLimit-1))
		}, kept: true},
		{name: "R15b 10000 heads", setup: func(t *testing.T, f rmFixture) {
			addPackedHeads(t, gitDir(f), refAt(t, realGit, f.repo, "wt1"), numbered("zz", branchListLimit-3))
		}},
		{name: "R17a gone path, a commit no other ref reaches", setup: func(t *testing.T, f rmFixture) {
			unique(t, f)
			if err := os.RemoveAll(f.wt); err != nil {
				t.Fatal(err)
			}
		}, kept: true},
		{name: "R17b gone path, main at the tip", setup: func(t *testing.T, f rmFixture) {
			if err := os.RemoveAll(f.wt); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "B2-04a the only local branch", setup: func(t *testing.T, f rmFixture) {
			unique(t, f)
			runGit(t, f.repo, "checkout", "-q", "--detach")
			runGit(t, f.repo, "update-ref", "-d", "refs/heads/main")
		}, kept: true},
		{name: "B2-04b the only local branch, origin/main at the tip", setup: func(t *testing.T, f rmFixture) {
			runGit(t, f.repo, "checkout", "-q", "--detach")
			runGit(t, f.repo, "update-ref", "refs/remotes/origin/main", "HEAD")
			runGit(t, f.repo, "update-ref", "-d", "refs/heads/main")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRmFixture(t)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			branch := tc.branch
			if branch == "" {
				branch = "wt1"
			}
			before := refAt(t, realGit, f.repo, "wt1")
			raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": branch})
			want := removeOK
			if tc.kept {
				want = removeKept
			}
			if raw != want {
				t.Errorf("reply = %s, want %s", raw, want)
			}
			mustBeGone(t, f.wt)
			switch {
			case tc.check != nil:
				tc.check(t, f, before)
			case tc.kept:
				if got := refAt(t, realGit, f.repo, "wt1"); got != before {
					t.Errorf("wt1 = %q, want it kept at %s", got, before)
				}
			default:
				if got := refAt(t, realGit, f.repo, "wt1"); got != "" {
					t.Errorf("wt1 = %s, want it deleted", got)
				}
			}
		})
	}
}

// TestWorktreeRemoveBranchStepFailures pins the kept rows that a failed or stopped
// git call gives. wt1 is at main's tip, so a check that ran to the end deletes it. A
// stub rule changes the one call under test.
func TestWorktreeRemoveBranchStepFailures(t *testing.T) {
	realGit := realGitPath(t)
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	for _, tc := range []struct {
		name         string
		branch       string
		arm          func(t *testing.T)
		kept         bool
		maxElapsed   time.Duration
		checkBound   time.Duration
		deleteBounds time.Duration
		d5           time.Duration
	}{
		// B2-03: rev-list exits 128.
		{name: "B2-03 rev-list fails", arm: func(t *testing.T) {
			stubRule2(t, "rev-list", "", 0, "128", "", "")
		}, kept: true},
		// R22a: for-each-ref runs past the bound, and is killed there.
		{name: "R22a for-each-ref stopped at the bound", checkBound: time.Second, arm: func(t *testing.T) {
			stubRule2(t, "for-each-ref", "", stubNever, "", "", "")
		}, kept: true, maxElapsed: stoppedCeiling},
		// A rev-parse stopped at the bound is not an absent branch, also on Windows,
		// where the kill gives exit code 1. That is claustrum's choice (not measured).
		{name: "rev-parse stopped at the bound", branch: "nosuch", checkBound: time.Second, arm: func(t *testing.T) {
			stubRule2(t, "rev-parse,--verify", "", stubNever, "", "", "")
		}, kept: true, maxElapsed: stoppedCeiling},
		// R22b: a for-each-ref under the bound finishes, and the check goes on.
		{name: "R22b for-each-ref under the bound", checkBound: 3 * time.Second, arm: func(t *testing.T) {
			stubRule2(t, "for-each-ref", "", 200*time.Millisecond, "", "", "")
		}},
		// B2-01: for-each-ref and rev-list share one bound. Each one alone fits in it,
		// the two together do not.
		{name: "B2-01 the bound is shared", checkBound: 1500 * time.Millisecond, arm: func(t *testing.T) {
			slowGit(t, "for-each-ref", "pre", 900*time.Millisecond, "", "")
			stubRule2(t, "rev-list", "", 900*time.Millisecond, "", "", "")
		}, kept: true},
		// R23a: update-ref is stopped at updateRefStop, so git never deletes wt1.
		// The update-ref stop counts from the start of update-ref, after its config
		// listing (row B2-09e: SIGTERM 5 s after update-ref started). A slow listing
		// therefore does not use up the stop, and the delete goes through. The stop
		// is 1 s, because update-ref itself must finish inside it: at 200 ms a
		// Windows host stopped a real update-ref in some runs (issue 463).
		{name: "the stop counts from the start of update-ref", deleteBounds: time.Second, arm: func(t *testing.T) {
			stubRule2(t, "config,-z,--list", "", 1500*time.Millisecond, "", "", "")
		}},
		// With -git-timeout (D5) opted in, a call of the branch step gets the D5
		// deadline too, and a stopped call keeps the branch. Not measured. Each git
		// call of the request gets this deadline, so it must be longer than the
		// slowest call before the branch step: at 3 s a Windows CI runner stopped an
		// earlier call (issue 463).
		{name: "D5 stops for-each-ref", d5: d5BranchDeadline, arm: func(t *testing.T) {
			stubRule2(t, "for-each-ref", "", stubNever, "", "", "")
		}, kept: true, maxElapsed: d5StoppedCeiling},
		{name: "R23a update-ref stopped", deleteBounds: 300 * time.Millisecond, arm: func(t *testing.T) {
			stubRule2(t, "update-ref", "", stubNever, "", "", "")
		}, kept: true, maxElapsed: stoppedCeiling},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRmFixture(t)
			before := refAt(t, realGit, f.repo, "wt1")
			check, del := 60*time.Second, 5*time.Second
			if tc.checkBound != 0 {
				check = tc.checkBound
			}
			if tc.deleteBounds != 0 {
				del = tc.deleteBounds
			}
			shortBounds(t, check, del, del)
			gitTimeout = tc.d5
			installGitSlowStub(t, realGit)
			tc.arm(t)
			branch := tc.branch
			if branch == "" {
				branch = "wt1"
			}
			start := time.Now()
			raw := removeFrame(t, map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": branch})
			elapsed := time.Since(start)
			want := removeOK
			if tc.kept {
				want = removeKept
			}
			if raw != want {
				t.Errorf("reply = %s, want %s", raw, want)
			}
			if tc.maxElapsed != 0 && elapsed > tc.maxElapsed {
				t.Errorf("reply after %v, want the bound to stop the call before %v", elapsed, tc.maxElapsed)
			}
			// The armed call sleeps for stubNever, so only the deadline ends it early.
			// A reply before the deadline means that another call kept the branch.
			if tc.d5 != 0 && elapsed < tc.d5 {
				t.Errorf("reply after %v, before the D5 deadline %v", elapsed, tc.d5)
			}
			mustBeGone(t, f.wt)
			got := refAt(t, realGit, f.repo, "wt1")
			if tc.kept && got != before {
				t.Errorf("wt1 = %q, want it kept at %s", got, before)
			}
			if !tc.kept && got != "" {
				t.Errorf("wt1 = %s, want it deleted", got)
			}
		})
	}
}

// TestWorktreeRemoveBranchStepSerialized pins rows R21 and R21b. Branches a and b both
// hold a commit c2 that no other ref reaches. Two removes arrive together, one through
// the repository and one through its linked worktree W3 (R21b) or the repository
// again (R21). The branch steps run one after the other, so the first sees the other
// branch at c2 and deletes its own. The second then keeps its branch. Exactly one
// branch stays, and its reply carries the member. The stub delays each update-ref, so
// that two steps at the same time would both list the other branch and both delete.
func TestWorktreeRemoveBranchStepSerialized(t *testing.T) {
	realGit := realGitPath(t)
	for _, viaLinked := range []bool{false, true} {
		name := "R21 same baseRepo"
		if viaLinked {
			name = "R21b baseRepo through a linked worktree"
		}
		t.Run(name, func(t *testing.T) {
			f := newRmFixture(t)
			base2 := f.repo
			if viaLinked {
				base2 = filepath.Join(filepath.Dir(f.repo), "W3")
				runGit(t, f.repo, "worktree", "add", "-q", "-b", "w3", base2)
			}
			wa := filepath.Join(f.repo, ".claude", "worktrees", "wa")
			wb := filepath.Join(base2, ".claude", "worktrees", "wb")
			runGit(t, f.repo, "worktree", "add", "-q", "-b", "a", wa)
			runGit(t, base2, "worktree", "add", "-q", "-b", "b", wb)
			runGit(t, wa, "commit", "-q", "--allow-empty", "-m", "c2")
			c2 := refAt(t, realGit, f.repo, "a")
			runGit(t, f.repo, "update-ref", "refs/heads/b", c2)

			installGitSlowStub(t, realGit)
			stubRule2(t, "update-ref", "", time.Second, "", "", "")
			s := newTestServer(t)
			raws := dispatchTogether(s, []string{
				rpcLine(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": wa, "branchName": "a"}),
				rpcLine(t, "git.worktree_remove", map[string]any{"baseRepo": base2, "worktreePath": wb, "branchName": "b"}),
			})
			mustBeGone(t, wa)
			mustBeGone(t, wb)
			refs := []string{refAt(t, realGit, f.repo, "a"), refAt(t, realGit, f.repo, "b")}
			keptCount := 0
			for i := range 2 {
				kept := raws[i] == removeKept
				if !kept && raws[i] != removeOK {
					t.Errorf("reply %d = %s, want %s or %s", i, raws[i], removeOK, removeKept)
				}
				if kept {
					keptCount++
					if refs[i] != c2 {
						t.Errorf("reply %d keeps its branch, but it is at %q, want %s", i, refs[i], c2)
					}
				} else if refs[i] != "" {
					t.Errorf("reply %d deletes its branch, but it is at %s", i, refs[i])
				}
			}
			if keptCount != 1 {
				t.Errorf("replies = %q, branches a=%q b=%q; want exactly one branch kept", raws, refs[0], refs[1])
			}
		})
	}
}

// TestWorktreeRemoveBranchStepConcurrent pins row B2-08: the branch steps of two
// different repositories run at the same time. Each step waits inside the lock until
// the other one is inside too. One lock for all repositories would stop the second
// step outside, and the first would wait in vain.
func TestWorktreeRemoveBranchStepConcurrent(t *testing.T) {
	realGit := realGitPath(t)
	f1, f2 := newRmFixture(t), newRmFixture(t)
	var inside atomic.Int32
	both := make(chan struct{})
	var once sync.Once
	var timedOut atomic.Bool
	branchStepEntered = func(string) {
		if inside.Add(1) == 2 {
			once.Do(func() { close(both) })
		}
		select {
		case <-both:
		case <-time.After(5 * time.Second):
			timedOut.Store(true)
		}
	}
	t.Cleanup(func() { branchStepEntered = nil })
	var lines []string
	for _, f := range []rmFixture{f1, f2} {
		runGit(t, f.wt, "commit", "-q", "--allow-empty", "-m", "c2")
		lines = append(lines, rpcLine(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.wt, "branchName": "wt1"}))
	}
	raws := dispatchTogether(newTestServer(t), lines)
	if n := inside.Load(); n != 2 {
		t.Errorf("the branch step hook ran %d times, want 2", n)
	}
	if timedOut.Load() {
		t.Error("the two branch steps did not run at the same time")
	}
	for i, f := range []rmFixture{f1, f2} {
		if raws[i] != removeKept {
			t.Errorf("reply %d = %s, want %s", i, raws[i], removeKept)
		}
		if refAt(t, realGit, f.repo, "wt1") == "" {
			t.Errorf("repository %d lost wt1, want it kept", i)
		}
	}
}

// dispatchTogether dispatches each line on its own goroutine, as the read loop of one
// connection does, and returns the replies in the order of lines.
func dispatchTogether(s *server, lines []string) []string {
	raws := make([]string, len(lines))
	var wg sync.WaitGroup
	for i, line := range lines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := s.dispatch(nil, []byte(line))
			if resp == nil {
				return
			}
			b, err := json.Marshal(*resp)
			if err != nil {
				raws[i] = "marshal: " + err.Error()
				return
			}
			raws[i] = string(b)
		}()
	}
	wg.Wait()
	return raws
}

// createFrame is the whole git.worktree_create failure frame.
func createFrame(t *testing.T, errText, code string, kept bool) string {
	t.Helper()
	tail := ""
	if kept {
		tail = `,"branchKept":true`
	}
	return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, errText) +
		`,"errorCode":"` + code + `"` + tail + `}}`
}

// The undo texts of 89cb6289, with the branch name w1 of the fixture.
const (
	undoHead       = "; and the undo could not finish for %s: "
	undoKeptText   = "branch w1 was left in place: it now has commits no other branch or remote-tracking ref reaches. To keep that work, push the branch or rename it. To discard it, run git branch -D w1. This branch name cannot be reused until the branch is renamed or deleted"
	undoUnchecked  = "branch w1 still exists; delete it by hand (for example: git branch -d w1, which git refuses if that would lose commits) before retrying this branch name: could not check whether another branch or remote-tracking ref reaches its commits"
	undoDelFailed  = "branch w1 still exists; delete it by hand (for example: git branch -D w1) before retrying this branch name"
	undoLockText   = ". A lock refs/heads/w1.lock is also present: if another git process is running against this repository, the lock may be live and clears on its own; if none is, it is stale debris of the interrupted delete — remove the lock file by hand too"
	checkoutFailed = "git worktree add failed (checkout): fatal: synthetic checkout failure"
)

// TestWorktreeCreateRollbackBranchStep pins the branch step of the create rollback
// after a failed checkout. The stub fails the read-tree, and its second rule changes a
// call of the branch step. HEAD is on main unless a row detaches it at a commit c2
// that no other ref reaches, so w1 starts at main's tip or at c2.
func TestWorktreeCreateRollbackBranchStep(t *testing.T) {
	realGit := realGitPath(t)
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	commonGit := func(f wtFixture) string { return filepath.Join(f.top, ".git") }
	for _, tc := range []struct {
		name    string
		branch  string // the branchName, w1 when empty
		unique  bool
		setup   func(t *testing.T, f wtFixture)
		arm     func(t *testing.T, f wtFixture)
		undo    string // after undoHead, or "" for no undo text
		kept    bool   // the member
		w1Stays bool
		bounded bool // a bound stops a stub call that never ends
	}{
		{name: "C01 main at the tip"},
		// X1 and X2: a name that the skip rule leaves alone stays, with its own text
		// and no member, whatever reaches it (f6010b97 and 89cb6289, Linux VM).
		{name: "X1 a name that starts with +", branch: "+x",
			undo: "branch +x left in place (its name is unsafe to pass to update-ref); delete it by hand", w1Stays: true},
		{name: "X2 a name that starts with +, a commit no other ref reaches", branch: "+x", unique: true,
			undo: "branch +x left in place (its name is unsafe to pass to update-ref); delete it by hand", w1Stays: true},
		{name: "C02 a commit no other ref reaches", unique: true,
			undo: "the worktree itself was removed, but " + undoKeptText, kept: true, w1Stays: true},
		{name: "C06 10001 heads", setup: func(t *testing.T, f wtFixture) {
			// main, ex and w1 make 10001 with these.
			addPackedHeads(t, commonGit(f), refAt(t, realGit, f.top, "main"), numbered("zz", branchListLimit-3))
		}, undo: undoUnchecked, w1Stays: true},
		{name: "C06c 10000 heads", setup: func(t *testing.T, f wtFixture) {
			addPackedHeads(t, commonGit(f), refAt(t, realGit, f.top, "main"), numbered("zz", branchListLimit-4))
		}},
		{name: "B2-09b for-each-ref fails", arm: func(t *testing.T, f wtFixture) {
			stubRule2(t, "for-each-ref", "", 0, "128", "", "")
		}, undo: undoUnchecked + " (exit status 128)", w1Stays: true},
		{name: "B2-09a for-each-ref stopped at the bound", arm: func(t *testing.T, f wtFixture) {
			shortBounds(t, time.Second, 5*time.Second, 5*time.Second)
			stubRule2(t, "for-each-ref", "", stubNever, "", "", "")
		}, undo: undoUnchecked + " (" + killedText() + ")", w1Stays: true, bounded: true},
		{name: "B2-09c a packed twin in another letter case", setup: func(t *testing.T, f wtFixture) {
			addPackedHeads(t, commonGit(f), refAt(t, realGit, f.top, "main"), []string{"W1"})
		}, undo: undoUnchecked, w1Stays: true},
		{name: "B2-09d the branch is gone before for-each-ref", arm: func(t *testing.T, f wtFixture) {
			stubRule2(t, "for-each-ref", "", 0, "", "rm", filepath.Join(commonGit(f), "refs", "heads", "w1"))
		}},
		{name: "B2-09f rev-list fails", arm: func(t *testing.T, f wtFixture) {
			stubRule2(t, "rev-list", "", 0, "128", "", "")
		}, undo: undoUnchecked + " (exit status 128)", w1Stays: true},
		{name: "C07a update-ref fails", arm: func(t *testing.T, f wtFixture) {
			stubRule2(t, "update-ref", "", 0, "128", "", "")
		}, undo: undoDelFailed + ": exit status 128", w1Stays: true},
		{name: "C07b update-ref fails on a lock", arm: func(t *testing.T, f wtFixture) {
			stubRule2(t, "update-ref", "", 0, "", "touch", filepath.Join(commonGit(f), "refs", "heads", "w1.lock"))
		}, undo: undoDelFailed + undoLockText + ": exit status 1", w1Stays: true},
		{name: "B2-09e update-ref stopped", arm: func(t *testing.T, f wtFixture) {
			shortBounds(t, 60*time.Second, 300*time.Millisecond, 300*time.Millisecond)
			stubRule2(t, "update-ref", "", stubNever, "", "", "")
		}, undo: undoDelFailed + ": " + stoppedText(), w1Stays: true, bounded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWTFixture(t, false)
			if tc.unique {
				runGit(t, f.top, "checkout", "-q", "--detach")
				runGit(t, f.top, "commit", "-q", "--allow-empty", "-m", "c2")
			}
			if tc.setup != nil {
				tc.setup(t, f)
			}
			tip := f.out(t, f.top, "rev-parse", "HEAD")
			installGitSlowStub(t, realGit)
			t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
			slowGit(t, "read-tree", "fail", 0, `fatal: synthetic checkout failure\n`, "")
			if tc.arm != nil {
				tc.arm(t, f)
			}
			branch := tc.branch
			if branch == "" {
				branch = "w1"
			}
			s := newTestServer(t)
			start := time.Now()
			raw, _ := f.create(t, s, branch, "", 0)
			if elapsed := time.Since(start); tc.bounded && elapsed > stoppedCeiling {
				t.Errorf("reply after %v, want the bound to stop the call before %v", elapsed, stoppedCeiling)
			}
			errText := checkoutFailed
			if tc.undo != "" {
				errText += fmt.Sprintf(undoHead, f.leaf()) + tc.undo
			}
			if want := createFrame(t, errText, "worktree_add_failed", tc.kept); raw != want {
				t.Errorf("reply = %s\nwant    %s", raw, want)
			}
			if got := f.leafEntries(t); got != nil {
				t.Errorf("leaf holds %q, want it gone", got)
			}
			if got := f.regs(t); len(got) != 0 {
				t.Errorf("registrations = %q, want none", got)
			}
			got := refAt(t, realGit, f.top, branch)
			if tc.w1Stays && got != tip {
				t.Errorf("%s = %q, want it kept at %s", branch, got, tip)
			}
			if !tc.w1Stays && got != "" {
				t.Errorf("%s = %s, want it deleted", branch, got)
			}
		})
	}
}

// TestWorktreeCreateTimeoutRollbackKeepsBranch pins row B2-10: the caller's deadline
// expires during the add, before the checkout. The rollback still runs the whole
// check, keeps w1 at the commit that no other ref reaches, and adds the member.
func TestWorktreeCreateTimeoutRollbackKeepsBranch(t *testing.T) {
	realGit := realGitPath(t)
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	f := newWTFixture(t, false)
	runGit(t, f.top, "checkout", "-q", "--detach")
	runGit(t, f.top, "commit", "-q", "--allow-empty", "-m", "c2")
	c2 := f.out(t, f.top, "rev-parse", "HEAD")
	installGitSlowStub(t, realGit)
	slowGit(t, "worktree,add", "post", 1500*time.Millisecond, "", "")
	raw, _ := f.create(t, newTestServer(t), "w1", "", 100)
	errText := "git worktree add timed out after 100ms (deadline expired before the checkout started)" +
		fmt.Sprintf(undoHead, f.leaf()) + "the worktree itself was removed, but " + undoKeptText
	if want := createFrame(t, errText, "timeout", true); raw != want {
		t.Errorf("reply = %s\nwant    %s", raw, want)
	}
	if got := refAt(t, realGit, f.top, "w1"); got != c2 {
		t.Errorf("w1 = %q, want it kept at %s", got, c2)
	}
	if got := f.leafEntries(t); got != nil {
		t.Errorf("leaf holds %q, want it gone", got)
	}
}
