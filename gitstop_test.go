package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Tests of the time limits of the git calls (gitstop.go). Every git call goes
// through the "git-slow" stub, the test binary itself. A held call sleeps in the
// stub before git runs, so a stop leaves no git work behind, as with the wrapper
// of round task010-01.

// testCallLimit is the limit of a call in these tests. It is long enough for the
// fast calls of a request on a loaded host. testHold is the time that a held call
// sleeps: far over testCallLimit, so a missing stop reads very differently.
const (
	testCallLimit = time.Second
	testHold      = 30 * time.Second
	// testDrainHold is the life of a descendant that holds the output of a call.
	testDrainHold = 12 * time.Second
	// testFastCeiling is a reply time that a request with a held call reaches
	// only when a limit stopped that call.
	testFastCeiling = 10 * time.Second
)

// shortGitLimit sets gitCallLimit to d for the test. The stop text keeps the words
// of the production limit.
func shortGitLimit(t *testing.T, d time.Duration) {
	t.Helper()
	old := gitCallLimit
	gitCallLimit = d
	t.Cleanup(func() { gitCallLimit = old })
}

// setDuration sets *v to d for the test.
func setDuration(t *testing.T, v *time.Duration, d time.Duration) {
	t.Helper()
	old := *v
	*v = d
	t.Cleanup(func() { *v = old })
}

// stopText is the stop text of a call of sub at the limit of a call.
func stopText(sub string) string {
	return "git took too long: Claude stopped git " + sub + " after 2 minutes; try again"
}

// stubLog sets the argv log of the git stub and returns its path.
func stubLog(t *testing.T) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
	return log
}

// wantLastCall fails the test unless the last call of the stub log holds every
// word of words: no git call ran after it.
func wantLastCall(t *testing.T, log string, words ...string) {
	t.Helper()
	if !lastCallHolds(t, log, words...) {
		calls := gitCalls(t, log)
		t.Fatalf("the last git call is %q, want the stopped call with %q: a call ran after the stop", calls[len(calls)-1], words)
	}
}

// The production values, pinned. The tests below shorten them through the same
// variables, so a changed default passes every other test.
func TestGitTimeLimitValues(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want time.Duration
	}{
		{"gitCallLimit (cells T1 to T7)", gitCallLimit, 120 * time.Second},
		{"gitWideLimit (claustrum's own, cells T8 and T9 show no stop at 2 minutes)", gitWideLimit, 1800 * time.Second},
		{"configListingLimit (cells T10 and T11)", configListingLimit, 30 * time.Second},
		{"includeScanLimit (cells T13 and T14)", includeScanLimit, 600 * time.Second},
		{"gitStopGrace (cell T2)", gitStopGrace, 2 * time.Second},
		{"gitDrainWait (cells T15 and T16)", gitDrainWait, 2 * time.Second},
		{"repoReadBound (cell T18)", repoReadBound, 30 * time.Second},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	var wide atomic.Int32
	s := &gitStop{wide: &wide}
	for _, c := range []struct {
		kind  gitLimit
		wide  int32
		want  time.Duration
		words string
	}{
		{callLimit, 0, 120 * time.Second, "2 minutes"},
		{callLimit, 1, 1800 * time.Second, "30 minutes"},
		{listingLimit, 0, 30 * time.Second, "30 seconds"},
		{listingLimit, 1, 30 * time.Second, "30 seconds"},
		{includeScan, 0, 600 * time.Second, "10 minutes"},
	} {
		wide.Store(c.wide)
		if d, words := s.limit(c.kind); d != c.want || words != c.words {
			t.Errorf("limit(%d) with %d requests of the window = %v, %q, want %v, %q", c.kind, c.wide, d, words, c.want, c.words)
		}
	}
}

// The stop text names the git subcommand: the first word that is no option and no
// value of -c or -C (cells T1, T3, T4, T5 and T10).
func TestGitSubcommand(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"git", "-c", "core.fsmonitor=", "-c", "core.hooksPath=/dev/null", "for-each-ref", "refs/heads/"}, "for-each-ref"},
		{[]string{"git", "--attr-source=4b82", "-c", "a=b", "--git-dir=/s/.gitdir-1", "--work-tree=/w", "status", "--porcelain"}, "status"},
		{[]string{"git", "-c", "x=y", "worktree", "add", "--no-checkout", "/l"}, "worktree"},
		{[]string{"git", "config", "-z", "--list"}, "config"},
		{[]string{"git", "-C", "/r", "rev-parse"}, "rev-parse"},
		{[]string{"git", "--no-literal-pathspecs", "ls-files", "-z"}, "ls-files"},
		{[]string{"git", "-c", "only=options"}, ""},
	} {
		if got := gitSubcommand(c.args); got != c.want {
			t.Errorf("gitSubcommand(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

// limitFixture is a repository with a held call of the git stub.
func limitFixture(t *testing.T, match string) (wtFixture, string) {
	t.Helper()
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	log := stubLog(t)
	slowGit(t, match, "pre", testHold, "", "")
	shortGitLimit(t, testCallLimit)
	return f, log
}

// dispatchTimed dispatches one request on s and returns its frame and time.
func dispatchTimed(t *testing.T, s *server, method string, params map[string]any) (string, time.Duration) {
	t.Helper()
	start := time.Now()
	raw := dispatchRaw(t, s, rpcLine(t, method, params))
	return raw, time.Since(start)
}

// errorFrame is the -32603 frame of id 1 with text.
func errorFrame(t *testing.T, text string) string {
	t.Helper()
	return `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` + jsonString(t, text) + `}}`
}

// The read methods answer -32603 with the stop text, and no git call follows the
// stopped call (round task010-01: cell T1 on Linux, macOS and Windows VMs, T3 on
// Linux and macOS VMs and T3h on a Windows VM, T4 on a Linux VM). git.info is the case that needs gitStop.reply: its failed `rev-parse
// --git-dir` answers "no repository" by itself.
func TestGitReadMethodsStopAtTheCallLimit(t *testing.T) {
	t.Run("git.list_branches stops for-each-ref (T1)", func(t *testing.T) {
		f, log := limitFixture(t, "for-each-ref")
		raw, took := dispatchTimed(t, newTestServer(t), "git.list_branches", map[string]any{"path": f.top})
		if want := errorFrame(t, stopText("for-each-ref")); raw != want {
			t.Fatalf("git.list_branches = %s\nwant %s", raw, want)
		}
		if took > testFastCeiling {
			t.Errorf("the reply took %v: the limit did not stop the held call", took)
		}
		wantLastCall(t, log, "for-each-ref")
	})
	t.Run("git.info stops the hardened rev-parse --git-dir (T3)", func(t *testing.T) {
		f, log := limitFixture(t, "protocol.allow=never,rev-parse,--git-dir")
		raw, _ := dispatchTimed(t, newTestServer(t), "git.info", map[string]any{"path": f.top})
		if want := errorFrame(t, stopText("rev-parse")); raw != want {
			t.Fatalf("git.info = %s\nwant %s", raw, want)
		}
		wantLastCall(t, log, "rev-parse", "--git-dir")
	})
	t.Run("git.status stops status --porcelain (T4)", func(t *testing.T) {
		f := newStatusFixture(t)
		installGitSlowStub(t, realGitPath(t))
		log := stubLog(t)
		slowGit(t, "status,--porcelain", "pre", testHold, "", "")
		shortGitLimit(t, testCallLimit)
		raw, _ := dispatchTimed(t, newTestServer(t), "git.status", map[string]any{"path": f.W, "baseRepo": f.T})
		if want := errorFrame(t, stopText("status")); raw != want {
			t.Fatalf("git.status = %s\nwant %s", raw, want)
		}
		wantLastCall(t, log, "status", "--porcelain")
	})
}

// A held `worktree add` gets the stop. The create answers worktree_add_failed with
// the stop text, the leaf, the registration and the new branch are gone, and no
// git call follows (round task010-01: cell T5 on Linux, macOS and Windows VMs, T6
// on Linux and macOS VMs, T7 on a Linux VM). The timeoutMs of the caller does not
// end the add (T6). In attach mode one add runs, and the branch stays (T7).
func TestWorktreeCreateAddStopsAtTheCallLimit(t *testing.T) {
	want := "git worktree add failed: " + stopText("worktree")
	for _, c := range []struct {
		name      string
		existing  string
		timeoutMs int
	}{
		{"new branch (T5)", "", 0},
		{"timeoutMs under the limit (T6)", "", 300},
		{"attach mode (T7)", "ex", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, log := limitFixture(t, "worktree,add")
			raw, took := f.createWith(t, newTestServer(t), "w1", c.timeoutMs, map[string]any{"existingBranch": c.existing})
			wantError(t, raw, want, "worktree_add_failed")
			if took < testCallLimit || took > testFastCeiling {
				t.Errorf("the reply took %v, want it at the stop of the add (%v)", took, testCallLimit)
			}
			if got := f.leafEntries(t); got != nil {
				t.Errorf("the leaf holds %q, want it gone", got)
			}
			if got := f.regs(t); len(got) != 0 {
				t.Errorf("registrations = %q, want none", got)
			}
			if f.hasRef(t, "w1") {
				t.Error("the branch w1 exists, want none")
			}
			if c.existing != "" && !f.hasRef(t, c.existing) {
				t.Errorf("the attached branch %s is gone, want it kept", c.existing)
			}
			if adds := gitCalls(t, log, "worktree", "add"); len(adds) != 1 {
				t.Errorf("worktree add calls = %d, want 1", len(adds))
			}
			wantLastCall(t, log, "worktree", "add")
		})
	}
}

// The wide limit holds while any request of the daemon with "shadowDirOff":true
// runs, also for a request without it on another repository. When the last such
// request answered, the limit of a call is back (round task010-01: cell T8 on
// Linux, macOS and Windows VMs, T9 on a Linux VM). The hold is 2 s and
// the limit of a call 1 s.
func TestGitWideLimitWhileAnOffRequestRuns(t *testing.T) {
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	slowGit(t, "for-each-ref", "pre", 2*time.Second, "", "")
	shortGitLimit(t, testCallLimit)
	s := newTestServer(t)
	ok := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"branches":["ex","main"]}}`
	t.Run("a request with the switch (T8)", func(t *testing.T) {
		raw, _ := dispatchTimed(t, s, "git.list_branches", map[string]any{"path": f.top, "shadowDirOff": true})
		if raw != ok {
			t.Fatalf("git.list_branches = %s\nwant %s", raw, ok)
		}
		if n := s.offGitRequests.Load(); n != 0 {
			t.Errorf("%d requests of the window after the answer, want 0", n)
		}
	})
	t.Run("a request without it while one with it runs (T9 R2)", func(t *testing.T) {
		// The other request is in flight: its count is what handleGit holds while it
		// runs.
		s.offGitRequests.Add(1)
		defer s.offGitRequests.Add(-1)
		raw, _ := dispatchTimed(t, s, "git.list_branches", map[string]any{"path": f.top})
		if raw != ok {
			t.Fatalf("git.list_branches = %s\nwant %s", raw, ok)
		}
	})
	t.Run("a request without it after the window (T9 R3)", func(t *testing.T) {
		raw, _ := dispatchTimed(t, s, "git.list_branches", map[string]any{"path": f.top})
		if want := errorFrame(t, stopText("for-each-ref")); raw != want {
			t.Fatalf("git.list_branches = %s\nwant %s", raw, want)
		}
	})
}

// A git call that exited while a descendant holds its output answers 2 s after the
// exit (cell T15 of round task010-01, Linux, macOS and Windows VMs). The descendant
// lives testDrainHold.
func TestGitDrainEndsAfterTheExit(t *testing.T) {
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	endHeldDescendant(t)
	slowGit(t, "for-each-ref", "hold", testDrainHold, "", "")
	raw, took := dispatchTimed(t, newTestServer(t), "git.list_branches", map[string]any{"path": f.top})
	if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"branches":["ex","main"]}}`; raw != want {
		t.Fatalf("git.list_branches = %s\nwant %s", raw, want)
	}
	if took > testDrainHold-4*time.Second {
		t.Errorf("the reply took %v: the daemon waited for the descendant", took)
	}
}

// The first config listing of a request ends at 30 s. The request answers the
// hooks refusal with the stop text, and no `git version` follows (cell T10 of
// round task010-01, Linux and macOS VMs, and cell T10w on a Windows VM). The limit is shortened here.
func TestGitFirstListingStops(t *testing.T) {
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	log := stubLog(t)
	t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "hold-other")
	setDuration(t, &configListingLimit, testCallLimit)
	raw, took := dispatchTimed(t, newTestServer(t), "git.list_branches", map[string]any{"path": f.top})
	want := "config-defined hooks could not be pinned off; git not run: listing the configuration in force: " +
		"git took too long: Claude stopped git config after 30 seconds; try again"
	if raw != errorFrame(t, want) {
		t.Fatalf("git.list_branches = %s\nwant %s", raw, errorFrame(t, want))
	}
	if took > testFastCeiling {
		t.Errorf("the reply took %v: the limit did not stop the listing", took)
	}
	if v := gitCalls(t, log, "version"); len(v) != 0 {
		t.Errorf("git version ran %d times after the stopped listing, want none", len(v))
	}
}

// A create answers the same refusal in error, with worktree_add_failed, and
// nothing is made (claustrum's own, not measured).
func TestWorktreeCreateFirstListingStops(t *testing.T) {
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "hold-other")
	setDuration(t, &configListingLimit, testCallLimit)
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	wantError(t, raw, "config-defined hooks could not be pinned off; git not run: listing the configuration in force: "+
		"git took too long: Claude stopped git config after 30 seconds; try again", "worktree_add_failed")
	if got := f.leafEntries(t); got != nil {
		t.Errorf("the leaf holds %q, want none", got)
	}
}

// The scan of the include step ends at 600 s. The create succeeds, nothing is
// copied, and no git call follows the scan (cell T13 of round task010-01, Linux
// and macOS VMs). The limit is shortened here.
func TestWorktreeCreateIncludeScanStops(t *testing.T) {
	f := newWTFixture(t, false)
	writeFile(t, filepath.Join(f.top, ".worktreeinclude"), ".env\n", 0o644)
	writeFile(t, filepath.Join(f.top, ".gitignore"), ".claude/worktrees/\n.claude/settings.local.json\n.env\n", 0o644)
	writeFile(t, filepath.Join(f.top, ".env"), "include-fixture\n", 0o644)
	installGitSlowStub(t, f.realGit)
	log := stubLog(t)
	slowGit(t, "ls-files,--others,--ignored,--directory", "pre", testHold, "", "")
	setDuration(t, &includeScanLimit, testCallLimit)
	raw, took := f.create(t, newTestServer(t), "w1", "", 0)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if took > testFastCeiling {
		t.Errorf("the create took %v: the limit did not stop the scan", took)
	}
	if _, err := os.Stat(filepath.Join(f.leaf(), ".env")); err == nil {
		t.Error("the leaf holds .env, want no copy after a stopped scan")
	}
	wantLastCall(t, log, "ls-files", "--directory")
}

// The timeoutMs of the caller does not end the listing before the checkout. The
// listing runs to its end, and then the timeout frame comes (cell T12 of round
// task010-01, Linux VM: the listing ran 10 s with a timeoutMs of 3000, with
// "shadowDirOff":true). Here the deadline is 2 s, and the listing is held 4 s.
func TestWorktreeCreateCheckoutListingOutlivesTimeoutMs(t *testing.T) {
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	const lateMs = 2000
	hold := 4 * time.Second
	log := stubLog(t)
	slowGit(t, "config,-z,--list", "pre", hold, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ARGEND", string(os.PathSeparator)+".git")
	raw, took := f.createWith(t, newTestServer(t), "w1", lateMs, map[string]any{"shadowDirOff": true})
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"git worktree add timed out after ` + strconv.Itoa(lateMs) +
		`ms (deadline expired during the checkout): context deadline exceeded","errorCode":"timeout"}}`
	if raw != want {
		t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if took < hold {
		t.Errorf("the reply came after %v, want it after the held listing (%v)", took, hold)
	}
	if rt := gitCalls(t, log, "read-tree"); len(rt) != 0 {
		t.Errorf("read-tree ran %d times, want none", len(rt))
	}
}

// The listing before the checkout ends at 30 s, after a good add. The create
// answers the checkout failure with the hooks refusal and rolls back (cell TA of
// round task010-02, Linux VM). The limit is shortened here.
func TestWorktreeCreateCheckoutListingStops(t *testing.T) {
	f := newWTFixture(t, false)
	installGitSlowStub(t, f.realGit)
	log := stubLog(t)
	// The full listing with --git-dir is the one of the checkout here.
	t.Setenv("CLAUSTRUM_GITSTUB_FAIL_LISTING", "hold-full")
	setDuration(t, &configListingLimit, testCallLimit)
	raw, took := f.create(t, newTestServer(t), "w1", "", 0)
	wantError(t, raw, "git worktree add failed (checkout): config-defined hooks could not be pinned off; git not run: "+
		"listing the configuration in force: git took too long: Claude stopped git config after 30 seconds; try again", "worktree_add_failed")
	if took > testFastCeiling {
		t.Errorf("the create took %v: the limit did not stop the listing", took)
	}
	f.assertRolledBack(t, []string{}, false)
	if rt := gitCalls(t, log, "read-tree"); len(rt) != 0 {
		t.Errorf("read-tree ran %d times, want none", len(rt))
	}
}

// endHeldDescendant ends the descendant of a "hold" stub call at the end of the
// test, so that the cleanup of its folders does not meet a held file.
func endHeldDescendant(t *testing.T) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "hold.pid")
	t.Setenv("CLAUSTRUM_GITSTUB_HOLDPID", pidFile)
	t.Cleanup(func() {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		for _, line := range strings.Fields(string(b)) {
			if pid, err := strconv.Atoi(line); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			}
		}
	})
}

// The listing of the repository config ends at 30 s. The request answers the
// refusal of the config with the read text (cell T18 of round task010-01, Linux
// VM). The process is killed with no SIGTERM first, so a listing that ignores
// SIGTERM ends at the bound too. The bound is shortened here.
func TestRepoConfigReadStops(t *testing.T) {
	f := newStatusFixture(t)
	installGitSlowStub(t, realGitPath(t))
	stubRule2(t, "config,--no-includes,--file,--list", "ignoreterm", testHold, "", "", "")
	setDuration(t, &repoReadBound, testCallLimit)
	raw, took := dispatchTimed(t, newTestServer(t), "git.status", map[string]any{"path": f.W, "baseRepo": f.T})
	want := repoConfigPrefix + readStopText(filepath.Join(f.T, ".git", "config"))
	if raw != errorFrame(t, want) {
		t.Fatalf("git.status = %s\nwant %s", raw, errorFrame(t, want))
	}
	if took > testFastCeiling {
		t.Errorf("the reply took %v: the bound did not end the listing", took)
	}
}

// offSwitchSet reads only the JSON value true of "shadowDirOff".
func TestOffSwitchSet(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"shadowDirOff":true}`:   true,
		`{"shadowDirOff":"true"}`: false,
		`{"shadowDirOff":1}`:      false,
		`{}`:                      false,
		`[1]`:                     false,
	} {
		if got := offSwitchSet(json.RawMessage(raw)); got != want {
			t.Errorf("offSwitchSet(%s) = %v, want %v", raw, got, want)
		}
	}
}

// A create whose repository test its time limit stopped answers the stop text as
// it is, with worktree_add_failed, and nothing is made (cell TI of round
// task010-04, Linux VM).
func TestWorktreeCreateRepositoryTestStops(t *testing.T) {
	f, log := limitFixture(t, "rev-parse,--is-inside-work-tree")
	raw, _ := f.create(t, newTestServer(t), "w1", "", 0)
	wantError(t, raw, stopText("rev-parse"), "worktree_add_failed")
	if got := f.leafEntries(t); got != nil {
		t.Errorf("the leaf holds %q, want none", got)
	}
	if f.hasRef(t, "w1") {
		t.Error("the branch w1 exists, want none")
	}
	wantLastCall(t, log, "rev-parse", "--is-inside-work-tree")
}

// worktreeConfigFixture is a repository with extensions.worktreeConfig and a
// config.worktree file, as in round task010-04, and a server with a run folder.
// The stub holds the nth listing of a config file for testHold. The listings
// before it are those of the start test and of the read of the repository
// config.
func worktreeConfigFixture(t *testing.T, nth int) (wtFixture, *server) {
	t.Helper()
	f := newWTFixture(t, false)
	runGit(t, f.top, "config", "extensions.worktreeConfig", "true")
	writeFile(t, filepath.Join(f.top, ".git", "config.worktree"), "[core]\n\tfilemode = false\n", 0o644)
	s, _ := newShadowServer(t)
	installGitSlowStub(t, f.realGit)
	slowGit(t, "config,--no-includes,--file,--list", "pre", testHold, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_NTHFILE", filepath.Join(t.TempDir(), "nth"))
	t.Setenv("CLAUSTRUM_GITSTUB_NTH", strconv.Itoa(nth))
	setDuration(t, &repoReadBound, testCallLimit)
	return f, s
}

// The listing of the config.worktree file after a good add ends at 30 s. The
// create answers the text of 5fd08069 and rolls back: leaf, registration and
// branch go (cells TW35 and TW130 of round task010-04, Linux VM).
func TestWorktreeCreateConfigWorktreeListingStops(t *testing.T) {
	f, s := worktreeConfigFixture(t, 5)
	raw, took := f.create(t, s, "w1", "", 0)
	wantError(t, raw, "git worktree add failed (completing the new worktree's git files): this repository's git config "+
		"cannot be used: git did not finish reading it within 30 seconds; try again", "worktree_add_failed")
	if took > testFastCeiling {
		t.Errorf("the create took %v: the bound did not end the listing", took)
	}
	f.assertRolledBack(t, []string{}, false)
}

// The listing of the config.worktree file of a linked worktree ends at 30 s in
// git.status. The frame stays {"isRepo":false,"clean":false} (cell TS of round
// task010-04, Linux VM).
func TestGitStatusConfigWorktreeListingStops(t *testing.T) {
	f, s := worktreeConfigFixture(t, 5)
	lw := filepath.Join(filepath.Dir(f.top), "LW")
	runGit(t, f.top, "worktree", "add", "-q", "-b", "lw", lw)
	writeFile(t, filepath.Join(f.top, ".git", "worktrees", "LW", "config.worktree"), "[core]\n\tfilemode = false\n", 0o644)
	raw, took := dispatchTimed(t, s, "git.status", map[string]any{"path": lw, "baseRepo": f.top})
	if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"clean":false}}`; raw != want {
		t.Fatalf("git.status = %s\nwant %s", raw, want)
	}
	if took > testFastCeiling {
		t.Errorf("git.status took %v: the bound did not end the listing", took)
	}
}

// The listing of the config.worktree file of a linked worktree in git.info has
// the same bound. The request then goes on with no items of that file
// (claustrum's own, not measured).
func TestGitInfoConfigWorktreeListingStops(t *testing.T) {
	f, s := worktreeConfigFixture(t, 5)
	lw := filepath.Join(filepath.Dir(f.top), "LW")
	runGit(t, f.top, "worktree", "add", "-q", "-b", "lw", lw)
	writeFile(t, filepath.Join(f.top, ".git", "worktrees", "LW", "config.worktree"), "[core]\n\tfilemode = false\n", 0o644)
	raw, took := dispatchTimed(t, s, "git.info", map[string]any{"path": lw})
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s, want a repository", raw)
	}
	if took > testFastCeiling {
		t.Errorf("git.info took %v: the bound did not end the listing", took)
	}
}
