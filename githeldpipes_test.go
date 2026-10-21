package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"
)

// The calls whose output the daemon reads to its end, and the config listing on
// standard input with one bound from its start (round task101-01, Linux VM). A
// child of the git stub holds the stdout and stderr of the matched call, as the
// wrapper of the round does. The limits are shortened here. Each test first
// sends its request with no held call, and then compares the reply.

// heldPipe is the life of the child that holds the pipes in these tests. It is
// longer than gitDrainWait, so the short wait for the pipes reads differently.
const heldPipe = 5 * time.Second

// holdNthCall makes the stub hold the pipes of call nth of those that match.
// With last set, a call must end with that word.
func holdNthCall(t *testing.T, match, mode, last string, nth int, hold time.Duration) {
	t.Helper()
	endHeldDescendant(t)
	slowGit(t, match, mode, hold, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ARGLAST", last)
	t.Setenv("CLAUSTRUM_GITSTUB_NTHFILE", filepath.Join(t.TempDir(), "nth"))
	t.Setenv("CLAUSTRUM_GITSTUB_NTH", strconv.Itoa(nth))
}

// holdHookListing holds the pipes of the first config listing before a hardened
// call. Such a listing ends with --list.
func holdHookListing(t *testing.T, mode string, hold time.Duration) {
	t.Helper()
	holdNthCall(t, "config,-z,--list", mode, "--list", 1, hold)
}

// callSevenEndedText matches the start-test text of a call 7 that its bound
// ended. The path in it is Go-quoted, so a Windows backslash shows doubled.
func callSevenEndedText(t *testing.T, f shadowFixture) *regexp.Regexp {
	t.Helper()
	inner := func(s string) string {
		q := strconv.Quote(s)
		return q[1 : len(q)-1]
	}
	return regexp.MustCompile("^" + regexp.QuoteMeta("Claude could not test git on the remote machine (this repository's git config cannot be used: git did not finish reading \""+
		inner(filepath.Join(daemonFolder(t, f.run), ".check-"))) + `[0-9]+` + regexp.QuoteMeta(inner(filepath.FromSlash("/real/.git/config"))+
		"\" within 30 seconds; try again). Check free disk space and quota there, then try again.") + "$")
}

// timedCall is f.call with the time of the reply.
func timedCall(t *testing.T, f shadowFixture, method string, params map[string]any) (string, []envCall, time.Duration) {
	t.Helper()
	start := time.Now()
	raw, calls := f.call(t, method, params)
	return raw, calls, time.Since(start)
}

// TestShadowHookListingWaitsForHeldPipes: the config listing before a hardened
// call exits 0 while a child holds its pipes past the 30 s limit. 5fd08069
// gives the normal reply when the pipes close (cell A1 of round task101-01,
// Linux VM: 45 s). Mutation: set the short wait for the pipes again in
// gitRunLimited.
func TestShadowHookListingWaitsForHeldPipes(t *testing.T) {
	f := newSeededShadowFixture(t)
	want, _ := f.info(t, f.repo, nil)
	setDuration(t, &configListingLimit, time.Second)
	holdHookListing(t, "hold", heldPipe)
	raw, _, took := timedCall(t, f, "git.info", map[string]any{"path": f.repo})
	if raw != want {
		t.Errorf("git.info = %s\nwant %s", raw, want)
	}
	if took < heldPipe {
		t.Errorf("the reply came after %v, want it after the child closed the pipes (%v)", took, heldPipe)
	}
}

// TestShadowHookListingStopTextAtPipeEOF: the listing still runs at its limit,
// and a child holds its pipes. git gets the stop, the child gets no signal, and
// the stop text comes when the pipes close (cell A2 of round task101-01, Linux
// VM: the reply at 60 s, when the child ended). Mutation: set the short wait
// for the pipes again in gitRunLimited.
func TestShadowHookListingStopTextAtPipeEOF(t *testing.T) {
	f := newSeededShadowFixture(t)
	setDuration(t, &configListingLimit, time.Second)
	holdHookListing(t, "holdwait", heldPipe)
	raw, _, took := timedCall(t, f, "git.info", map[string]any{"path": f.repo})
	want := errorFrame(t, "config-defined hooks could not be pinned off; git not run: listing the configuration in force: "+
		"git took too long: Claude stopped git config after 30 seconds; try again")
	if raw != want {
		t.Errorf("git.info = %s\nwant %s", raw, want)
	}
	if took < heldPipe {
		t.Errorf("the reply came after %v, want it after the child closed the pipes (%v)", took, heldPipe)
	}
}

// TestShadowHookListingFailedExitPastLimit: the listing exits 1 at once, and a
// child holds its pipes past the limit. The call counts as stopped at the
// limit, so the stop text comes when the pipes close, on every system
// (claustrum's own, not measured). Mutation: do not mark the call as stopped
// in gitRunLimited when the stop finds git gone (os.ErrProcessDone).
func TestShadowHookListingFailedExitPastLimit(t *testing.T) {
	f := newSeededShadowFixture(t)
	setDuration(t, &configListingLimit, time.Second)
	holdHookListing(t, "holdfail", heldPipe)
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "1")
	raw, _, took := timedCall(t, f, "git.info", map[string]any{"path": f.repo})
	want := errorFrame(t, "config-defined hooks could not be pinned off; git not run: listing the configuration in force: "+
		"git took too long: Claude stopped git config after 30 seconds; try again")
	if raw != want {
		t.Errorf("git.info = %s\nwant %s", raw, want)
	}
	if took < heldPipe {
		t.Errorf("the reply came after %v, want it after the child closed the pipes (%v)", took, heldPipe)
	}
}

// TestGitStatusScansWaitForHeldPipes: each of the three scans of git.status
// exits 0 while a child holds its pipes. The reply is the normal one, after
// the pipes close. 5fd08069 does so for `status` (cell A3 of round
// task101-01, Linux VM). The other two scans are not measured. Mutation: drop
// the untilPipeEOF of (*statusRun).git.
func TestGitStatusScansWaitForHeldPipes(t *testing.T) {
	for _, match := range []string{"status,--porcelain", "ls-files,-s,-z", "diff-index,--cached"} {
		t.Run(match, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			m := f.fiveRequests(nil)[1]
			want, _ := f.call(t, m.method, m.params)
			holdNthCall(t, match, "hold", "", 1, heldPipe)
			raw, _, took := timedCall(t, f, m.method, m.params)
			if raw != want {
				t.Errorf("git.status = %s\nwant %s", raw, want)
			}
			if took < heldPipe {
				t.Errorf("the reply came after %v, want it after the child closed the pipes (%v)", took, heldPipe)
			}
		})
	}
}

// TestWorktreeCreateFilterListingWaitsForHeldPipes: the listing right before
// the read-tree call exits 0 while a child holds its pipes. The read-tree call
// starts when the pipes close, and the create succeeds (cell A4 of round
// task101-01, Linux VM: read-tree 10.1 s after the exit). Mutation: drop the
// cmd.Stdout of ownConfigListing.
func TestWorktreeCreateFilterListingWaitsForHeldPipes(t *testing.T) {
	f := newCheckoutRuleFixture(t)
	slowGit(t, "config,-z,--list", "hold", heldPipe, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_INDEX_FILE=index")
	endHeldDescendant(t)
	start := time.Now()
	raw, calls := f.create(t, nil)
	took := time.Since(start)
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonString(t, f.leaf) + `,"sourceBranch":"main","branch":"w1"}}`
	if raw != want {
		t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if !ranReadTree(calls) {
		t.Error("no read-tree call ran")
	}
	if took < heldPipe {
		t.Errorf("the create took %v, want the read-tree call after the child closed the pipes (%v)", took, heldPipe)
	}
}

// TestGitStatusRepoConfigReadBoundFromStart: the listing of the repository
// config on standard input exits 0, and a child holds its pipes past the bound.
// The bound counts from the start of the call, so git.status answers the read
// text and makes no folder of the repository in the settings copy (cell B1 of
// round task101-01, Linux VM). Mutation: in runStartBound, kill only a git
// that still runs at the bound, and wait for the pipes with no bound.
func TestGitStatusRepoConfigReadBoundFromStart(t *testing.T) {
	f := newSeededShadowFixture(t)
	setDuration(t, &repoReadBound, time.Second)
	holdNthCall(t, "config,--no-includes,--file,--list", "hold", "", 1, 3*heldPipe)
	m := f.fiveRequests(nil)[1]
	raw, _, took := timedCall(t, f, m.method, m.params)
	want := errorFrame(t, repoConfigPrefix+readStopText(filepath.Join(f.gitDir, "config")))
	if raw != want {
		t.Errorf("git.status = %s\nwant %s", raw, want)
	}
	if took > 2*heldPipe {
		t.Errorf("the reply came after %v, want it at the bound, before the child ended (%v)", took, 3*heldPipe)
	}
	if slices.Contains(entryNames(t, daemonFolder(t, f.run)), shadowRepoName(f.gitDir)) {
		t.Error("the settings copy holds a folder of the repository, want none")
	}
}

// TestShadowCallSevenBoundFromStart: call 7 of the start test exits 0 after a
// time, and a child holds its pipes. The bound counts from the start of the
// call, not from its exit, and the test fails with the read text (cell B2 of
// rounds task101-01 and task101-02, Linux VM: the reply about 31 s after the
// call started).
// Call 7 exits only shortly before the bound, so a bound counted from its exit
// ends at least callSevenExit later. The wait for the pipes of the other test
// calls is long here, so a bound after the exit reads differently there too.
// The time counts from the start of call 7, which the stub writes. 5fd08069
// answers about 1 s after the bound, and so does the drain of runStartBound.
// Mutation: give call 7 a WaitDelay of shadowCallBound+shadowCallDrain, run it
// through boundedGitInput, start the bound at the exit of git, or close the
// pipes at the bound with no drain.
func TestShadowCallSevenBoundFromStart(t *testing.T) {
	const callSevenExit = 2500 * time.Millisecond
	f := newShadowFixture(t)
	setDuration(t, &shadowCallBound, 3*time.Second)
	setDuration(t, &shadowCallDrain, 2*heldPipe)
	holdNthCall(t, "--no-includes", "holdwait", "", 1, 4*heldPipe)
	t.Setenv("CLAUSTRUM_GITSTUB_WAITMS", strconv.Itoa(int(callSevenExit/time.Millisecond)))
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("CLAUSTRUM_GITSTUB_STARTED", started)
	msg := f.s.gitShadow.gate(false)
	end := time.Now()
	want := callSevenEndedText(t, f)
	if !want.MatchString(msg) {
		t.Errorf("the start test = %q\nwant %s", msg, want)
	}
	text, err := os.ReadFile(started)
	if err != nil {
		t.Fatalf("call 7 did not start: %v", err)
	}
	ns, err := strconv.ParseInt(string(text), 10, 64)
	if err != nil {
		t.Fatalf("the start time of call 7 = %q: %v", text, err)
	}
	took := end.Sub(time.Unix(0, ns))
	if took > shadowCallBound+startBoundDrain+time.Second {
		t.Errorf("the start test ended %v after call 7 started, want it at the bound from that start", took)
	}
	if took < shadowCallBound+startBoundDrain/2 {
		t.Errorf("the start test ended %v after call 7 started, want it after the bound and the drain of %v", took, startBoundDrain)
	}
}

// TestProbeAgainAfterALongHookListing: the kept answer of the call about the
// repository is old after a hook listing that waited more than probeKeep for
// its pipes, so a second such call follows. After a short wait none follows.
// 5fd08069 makes the second call after the 45 s wait of cell A1 of round
// task101-01 (Linux VM). Mutation: set the short wait for the pipes again in
// gitRunLimited.
func TestProbeAgainAfterALongHookListing(t *testing.T) {
	for _, row := range []struct {
		name string
		hold time.Duration
		want int
	}{{"long", probeKeep + 2*time.Second, 2}, {"short", time.Second, 1}} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			holdHookListing(t, "hold", row.hold)
			_, calls := f.info(t, f.repo, nil)
			if got := len(probeCalls(calls)); got != row.want {
				t.Errorf("after a listing held %v: %d calls about the repository, want %d", row.hold, got, row.want)
			}
		})
	}
}

// TestStartTestCallsAndTheirPipes pins three rows of val-s3p2b2-8-linux2 (Linux
// VM), with lowered times:
//
//   - N-a: a child keeps the output pipes of the `init` call open. The test
//     goes on after the wait for the pipes and passes. Without the rule of
//     exitedInTime it fails with a `WaitDelay expired` text.
//   - N-b: the same child on call 7, the listing of the test config. The test
//     waits until the pipes close and passes. With the short wait of the other
//     calls it ends before the child does.
//   - N-c: call 7 runs past the bound. The cause is the text of 5fd08069 with
//     the config of the test repository. Without the test of an ended call
//     the cause is `git config failed under …: signal: killed`.
//
// A child that holds the pipes of call 7 past the bound is the row of
// TestShadowCallSevenBoundFromStart.
func TestStartTestCallsAndTheirPipes(t *testing.T) {
	t.Run("N-a init", func(t *testing.T) {
		f := newShadowFixture(t)
		endHeldDescendant(t)
		// The child holds the pipes for three times the bound. A start test that
		// waits for the pipes ends at the bound or later. One that does not ends
		// about 20 s before the bound, so a slow host does not fail the check.
		setDuration(t, &shadowCallBound, 20*time.Second)
		setDuration(t, &shadowCallDrain, 200*time.Millisecond)
		slowGit(t, "init", "hold", 3*shadowCallBound, "", "")
		start := time.Now()
		if msg := f.s.gitShadow.gate(false); msg != "" {
			t.Errorf("the start test = %q, want it to pass", msg)
		}
		if d := time.Since(start); d >= shadowCallBound {
			t.Errorf("the start test ended after %v, want it before the bound of %v", d, shadowCallBound)
		}
	})
	t.Run("N-b call 7", func(t *testing.T) {
		f := newShadowFixture(t)
		endHeldDescendant(t)
		setDuration(t, &shadowCallBound, 20*time.Second)
		setDuration(t, &shadowCallDrain, 200*time.Millisecond)
		slowGit(t, "--no-includes", "hold", 1500*time.Millisecond, "", "")
		start := time.Now()
		if msg := f.s.gitShadow.gate(false); msg != "" {
			t.Errorf("the start test = %q, want it to pass", msg)
		}
		if d := time.Since(start); d < 1400*time.Millisecond {
			t.Errorf("the start test ended after %v, want it to wait for the pipes of call 7", d)
		}
	})
	t.Run("N-c call 7 past the bound", func(t *testing.T) {
		f := newShadowFixture(t)
		endHeldDescendant(t)
		setDuration(t, &shadowCallBound, time.Second)
		setDuration(t, &shadowCallDrain, 200*time.Millisecond)
		slowGit(t, "--no-includes", "pre", 20*time.Second, "", "")
		msg := f.s.gitShadow.gate(false)
		want := callSevenEndedText(t, f)
		if !want.MatchString(msg) {
			t.Errorf("the start test = %q\nwant %s", msg, want)
		}
	})
}
