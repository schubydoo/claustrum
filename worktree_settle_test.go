package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests of the settle step of git.worktree_create (worktreesettle.go). The
// diff-files call goes to the "fail" mode of the git stub, which answers its exit
// code with no real git.

// settleCall is one git call of the CTXLOG of the stub.
type settleCall struct {
	cwd, index string
	argv       []string
}

func readSettleCalls(t *testing.T, log string) []settleCall {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var calls []settleCall
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1e")
		if len(parts) != 5 {
			t.Fatalf("CTXLOG line %q has %d fields, want 5", line, len(parts))
		}
		calls = append(calls, settleCall{cwd: parts[0], index: parts[1], argv: strings.Split(parts[2], "\x1f")})
	}
	return calls
}

// settleFixture is a repository, a server in the state shadow and the git stub
// with its CTXLOG. The diff-files call exits with code after hold. The probe of
// --attr-source counts as done with the answer supported, so the host git
// does not decide it.
func settleFixture(t *testing.T, code int, hold time.Duration) (wtFixture, *server, string) {
	t.Helper()
	f := newWTFixture(t, false)
	s, _ := newShadowServer(t)
	installGitSlowStub(t, f.realGit)
	setAttrSource(t, true)
	log := filepath.Join(t.TempDir(), "ctx.log")
	t.Setenv("CLAUSTRUM_GITSTUB_CTXLOG", log)
	slowGit(t, "diff-files", "fail", hold, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", strconv.Itoa(code))
	return f, s, log
}

// setAttrSource makes the probe of attrSourceArgs count as done with the answer
// ok. The cleanup clears it.
func setAttrSource(t *testing.T, ok bool) {
	t.Helper()
	attrSourceMu.Lock()
	attrSourceDone, attrSourceOK = true, ok
	attrSourceMu.Unlock()
	t.Cleanup(clearAttrSource)
}

// clearAttrSource makes the next use of attrSourceArgs run the probe.
func clearAttrSource() {
	attrSourceMu.Lock()
	attrSourceDone, attrSourceOK = false, false
	attrSourceMu.Unlock()
}

// wantSettledCreate fails the test unless raw is a success and the worktree, its
// registration with an index and the branch w1 are there.
func (f wtFixture) wantSettledCreate(t *testing.T, raw string) {
	t.Helper()
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if _, err := os.Stat(filepath.Join(f.leaf(), "t.txt")); err != nil {
		t.Errorf("the checked-out file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.regDir, "w1", "index")); err != nil {
		t.Errorf("the index of the registration: %v", err)
	}
	if !f.hasRef(t, "w1") {
		t.Error("the branch w1 is gone")
	}
}

// diffFilesCalls returns the settle calls of calls.
func diffFilesCalls(calls []settleCall) []settleCall {
	var out []settleCall
	for _, c := range calls {
		if slices.Contains(c.argv, "diff-files") {
			out = append(out, c)
		}
	}
	return out
}

// argValue is the value of the argument of argv with prefix, or "".
func argValue(argv []string, prefix string) string {
	for _, a := range argv {
		if v, ok := strings.CutPrefix(a, prefix); ok {
			return v
		}
	}
	return ""
}

// The call after the read-tree call: the working directory and GIT_INDEX_FILE of
// the checkout, the argv of 683cc6a9, and no wait after exit 0 (Linux, macOS and
// Windows VMs).
func TestWorktreeCreateSettleCallAfterTheCheckout(t *testing.T) {
	f, s, log := settleFixture(t, 0, 0)
	setDuration(t, &settleWait, testHold)
	raw, took := f.create(t, s, "w1", "", 0)
	f.wantSettledCreate(t, raw)
	if took >= testHold {
		t.Errorf("the create took %v: the reply waited after exit 0", took)
	}
	calls := readSettleCalls(t, log)
	var readTree *settleCall
	for i := range calls {
		if slices.Contains(calls[i].argv, "read-tree") {
			readTree = &calls[i]
		}
	}
	diff := diffFilesCalls(calls)
	if readTree == nil || len(diff) != 1 {
		t.Fatalf("read-tree call %v, diff-files calls %d: want one of each", readTree != nil, len(diff))
	}
	c := diff[0]
	if !argvEndsWith(c.argv, []string{"diff-files", "--quiet", "--ignore-submodules=all"}) {
		t.Errorf("diff-files argv = %q", c.argv)
	}
	if c.argv[0] != "--attr-source="+gitEmptyTree {
		t.Errorf("first argument = %q, want the --attr-source option", c.argv[0])
	}
	if c.index == "" || c.index != readTree.index {
		t.Errorf("GIT_INDEX_FILE = %q, want the one of the read-tree call %q", c.index, readTree.index)
	}
	if !sameDir(c.cwd, f.leaf()) || c.cwd != readTree.cwd {
		t.Errorf("working directory = %q, want the new worktree %q", c.cwd, f.leaf())
	}
	for _, p := range []string{"--git-dir=", "--work-tree="} {
		if v := argValue(readTree.argv, p); v == "" || !slices.Contains(c.argv, p+v) {
			t.Errorf("diff-files argv %q has not the %s value %q of the read-tree call", c.argv, p, v)
		}
	}
}

// Exit 1 makes the reply wait (Linux, macOS and Windows VMs). The wait ends
// settleWait after the read-tree call, so the reply comes no sooner than that
// after the request.
func TestWorktreeCreateSettleWaitsAfterExit1(t *testing.T) {
	f, s, log := settleFixture(t, 1, 0)
	const wait = 5 * time.Second
	setDuration(t, &settleWait, wait)
	setDuration(t, &settleWaitWhole, wait)
	raw, took := f.create(t, s, "w1", "", 0)
	f.wantSettledCreate(t, raw)
	if n := len(diffFilesCalls(readSettleCalls(t, log))); n != 1 {
		t.Fatalf("diff-files calls = %d, want 1", n)
	}
	if took < wait {
		t.Errorf("the create took %v, want at least the wait %v after exit 1", took, wait)
	}
}

// With less than the margin left before the deadline of timeoutMs, no call runs
// (timeoutMs 10000, Linux VM). A failed --attr-source probe runs no call either
// (Linux VM). The create succeeds in both.
func TestWorktreeCreateSettleSkipped(t *testing.T) {
	t.Run("margin", func(t *testing.T) {
		f, s, log := settleFixture(t, 1, 0)
		setDuration(t, &settleMargin, time.Hour)
		raw, _ := f.create(t, s, "w1", "", markerTimeoutMs)
		f.wantSettledCreate(t, raw)
		if n := len(diffFilesCalls(readSettleCalls(t, log))); n != 0 {
			t.Errorf("diff-files calls = %d, want none", n)
		}
	})
	t.Run("no attr-source", func(t *testing.T) {
		f, s, log := settleFixture(t, 1, 0)
		setAttrSource(t, false)
		raw, _ := f.create(t, s, "w1", "", 0)
		f.wantSettledCreate(t, raw)
		if n := len(diffFilesCalls(readSettleCalls(t, log))); n != 0 {
			t.Errorf("diff-files calls = %d, want none", n)
		}
	})
}

// A call that its limit stops changes nothing: the create answers success and
// keeps the worktree, its registration and the branch (timeoutMs 11500, Linux).
// The margin here leaves 15 s before the call, so a slow host still makes it,
// and the call gets less than its hold.
func TestWorktreeCreateSettleStopKeepsTheCreate(t *testing.T) {
	const hold = 50 * time.Second
	f, s, log := settleFixture(t, 1, hold)
	setDuration(t, &settleMargin, 45*time.Second)
	raw, took := f.create(t, s, "w1", "", markerTimeoutMs)
	f.wantSettledCreate(t, raw)
	if n := len(diffFilesCalls(readSettleCalls(t, log))); n != 1 {
		t.Fatalf("diff-files calls = %d, want 1", n)
	}
	if took >= hold-5*time.Second {
		t.Errorf("the create took %v: the limit did not stop the call", took)
	}
}

// The limit of the call: none under the margin, the time over the margin, and at
// least settleMinLimit (timeoutMs 10000, 11500 and 15000, Linux). With no
// deadline it is the limit of the other calls of the request: the wide limit
// while a "shadowDirOff":true request runs. With much time left it is no more than
// that default (claustrum's own, not measured). Mutation: drop the min with the
// default in settleLimit.
func TestSettleLimit(t *testing.T) {
	wide := &atomic.Int32{}
	for _, c := range []struct {
		name string
		s    *gitStop
		n    int32
		want time.Duration
	}{
		{"no limits", nil, 0, gitCallLimit},
		{"the call limit", &gitStop{wide: wide}, 0, gitCallLimit},
		{"the wide limit", &gitStop{wide: wide}, 1, gitWideLimit},
	} {
		wide.Store(c.n)
		if got, ok := settleLimit(context.Background(), c.s); !ok || got != c.want {
			t.Errorf("no deadline, %s: %v %v, want %v true", c.name, got, ok, c.want)
		}
	}
	for _, c := range []struct {
		left   time.Duration
		ok     bool
		lo, hi time.Duration
	}{
		{9300 * time.Millisecond, false, 0, 0},
		{10600 * time.Millisecond, true, settleMinLimit, settleMinLimit},
		{14 * time.Second, true, 3500 * time.Millisecond, 4 * time.Second},
		{gitCallLimit + time.Minute, true, gitCallLimit, gitCallLimit},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), c.left)
		got, ok := settleLimit(ctx, nil)
		cancel()
		if ok != c.ok || got < c.lo || got > c.hi {
			t.Errorf("%v left: %v %v, want %v in [%v, %v]", c.left, got, ok, c.ok, c.lo, c.hi)
		}
	}
}

// git.status of the new worktree does not change the mtime of the index of the
// registration, and its git calls carry no GIT_INDEX_FILE (Linux VM). The frame
// is {"isRepo":true,"clean":true}.
func TestGitStatusKeepsTheRegistrationIndexTime(t *testing.T) {
	f, s, log := settleFixture(t, 0, 0)
	raw, _ := f.create(t, s, "w1", "", 0)
	f.wantSettledCreate(t, raw)
	idx := filepath.Join(f.regDir, "w1", "index")
	before, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// The status takes the answer of the host git.
	clearAttrSource()
	status, _ := dispatchTimed(t, s, "git.status", map[string]any{"path": f.leaf(), "baseRepo": f.base})
	if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"clean":true}}`; status != want {
		t.Fatalf("git.status = %s\nwant %s", status, want)
	}
	after, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("index mtime %v after git.status, want %v", after.ModTime(), before.ModTime())
	}
	for _, c := range readSettleCalls(t, log) {
		if c.index != "" {
			t.Errorf("git call %q has GIT_INDEX_FILE=%s, want none", c.argv, c.index)
		}
	}
}

// The wait after exit 1 is settleWait when `.git` in the leaf has an mtime with
// nanoseconds, and settleWaitWhole when its mtime is a whole second or the Lstat
// fails (claustrum's own, not measured). Mutation: return settleWait from
// settleWaitFor for a whole second, or for a failed Lstat.
func TestSettleWaitFor(t *testing.T) {
	leaf := t.TempDir()
	gitFile := filepath.Join(leaf, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		ns   int
		want time.Duration
	}{
		{"nanoseconds", 123456789, settleWait},
		{"whole second", 0, settleWaitWhole},
	} {
		mtime := time.Date(2026, 10, 31, 12, 0, 0, c.ns, time.UTC)
		if err := os.Chtimes(gitFile, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(gitFile)
		if err != nil {
			t.Fatal(err)
		}
		if c.ns != 0 && fi.ModTime().Nanosecond() == 0 {
			t.Skip("the file system keeps no nanoseconds")
		}
		if got := settleWaitFor(leaf); got != c.want {
			t.Errorf("%s: wait %v, want %v", c.name, got, c.want)
		}
	}
	if got := settleWaitFor(filepath.Join(leaf, "missing")); got != settleWaitWhole {
		t.Errorf("failed Lstat: wait %v, want %v", got, settleWaitWhole)
	}
}

// The wait ends when the context of the request ends (claustrum's own, not
// measured). Mutation: replace settleSleep with a time.Sleep.
func TestSettleSleepStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	settleSleep(ctx, start.Add(5*time.Second))
	if took := time.Since(start); took >= 2*time.Second {
		t.Errorf("the wait took %v after the context ended", took)
	}
}

// setSettleWaitFor makes settleWaitFor answer d and record its leaf in *leaf until
// the test ends.
func setSettleWaitFor(t *testing.T, d time.Duration, leaf *string) {
	t.Helper()
	old := settleWaitFor
	settleWaitFor = func(l string) time.Duration {
		*leaf = l
		return d
	}
	t.Cleanup(func() { settleWaitFor = old })
}

// After exit 1 the create waits the wait that settleWaitFor gives for the new
// worktree leaf, not settleWait (claustrum's own, not measured). Mutation: use
// settleWait in place of settleWaitFor(leaf) in settleCheckout.
func TestWorktreeCreateSettleUsesTheLeafWait(t *testing.T) {
	f, s, _ := settleFixture(t, 1, 0)
	const wait = 5 * time.Second
	setDuration(t, &settleWait, 0)
	var leaf string
	setSettleWaitFor(t, wait, &leaf)
	raw, took := f.create(t, s, "w1", "", 0)
	f.wantSettledCreate(t, raw)
	if !sameDir(leaf, f.leaf()) {
		t.Errorf("settleWaitFor got leaf %q, want %q", leaf, f.leaf())
	}
	if took < wait {
		t.Errorf("the create took %v, want at least the leaf wait %v", took, wait)
	}
}

// settleCheckout stops its wait when the context of the request ends (claustrum's
// own, not measured). Mutation: pass context.Background() to settleSleep in
// settleCheckout.
func TestSettleCheckoutStopsWithTheContext(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}
	installGitSlowStub(t, realGit)
	setAttrSource(t, true)
	slowGit(t, "diff-files", "fail", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "1")
	var leaf string
	setSettleWaitFor(t, 20*time.Second, &leaf)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	stop := time.AfterFunc(100*time.Millisecond, cancel)
	defer stop.Stop()
	settleCheckout(ctx, gitUse{}, dir, dir, filepath.Join(dir, "g"), filepath.Join(dir, "index"), nil, start)
	if leaf != dir {
		t.Fatalf("settleWaitFor got leaf %q, want %q: the wait did not run", leaf, dir)
	}
	if took := time.Since(start); took >= 10*time.Second {
		t.Errorf("settleCheckout took %v after the context ended", took)
	}
}

// The settle call stops timeoutMs less 10 s after the send, and at least 1 s after
// its start (683cc6a9, Linux VM). Mutation: drop the time.Since(arrival) in
// worktreeCreateCtx.
func TestWorktreeCreateCtxCountsFromArrival(t *testing.T) {
	now := time.Now()
	_, caller, cancel := worktreeCreateCtx(nil, 12000, now.Add(-3*time.Second))
	d, set := caller.Deadline()
	cancel()
	if want := now.Add(9 * time.Second); !set || d.Before(want.Add(-time.Second)) || d.After(want.Add(500*time.Millisecond)) {
		t.Errorf("deadline %v after now (set %v), want about 9s: 12 s from an arrival 3 s ago", d.Sub(now), set)
	}
	_, caller, cancel = worktreeCreateCtx(nil, 12000, now.Add(-20*time.Second))
	if caller.Err() == nil || !callerTimeoutFired(caller) {
		t.Errorf("a deadline that passed before the context: err %v, want the caller's deadline", caller.Err())
	}
	cancel()
	for _, c := range []struct {
		ms     int
		since  time.Duration
		ok     bool
		lo, hi time.Duration
	}{
		{10700, 830 * time.Millisecond, false, 0, 0},
		{12000, 830 * time.Millisecond, true, 900 * time.Millisecond, 1170 * time.Millisecond},
		{11500, 1300 * time.Millisecond, true, settleMinLimit, settleMinLimit},
		{20000, 816 * time.Millisecond, true, 8500 * time.Millisecond, 9184 * time.Millisecond},
	} {
		_, caller, cancel := worktreeCreateCtx(nil, c.ms, time.Now().Add(-c.since))
		got, ok := settleLimit(caller, nil)
		cancel()
		if ok != c.ok || got < c.lo || got > c.hi {
			t.Errorf("timeoutMs %d after %v: %v %v, want %v in [%v, %v]", c.ms, c.since, got, ok, c.ok, c.lo, c.hi)
		}
	}
}

// A slow git call before the deadline is made does not move its end: the settle
// call stops timeoutMs less 10 s after the send (683cc6a9, Linux VM). Mutation:
// drop the time.Since(arrival) in worktreeCreateCtx.
func TestWorktreeCreateDeadlineCountsFromTheRequest(t *testing.T) {
	const delay = 1500 * time.Millisecond
	f, s, _ := settleFixture(t, 0, 0)
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "--absolute-git-dir")
	t.Setenv("CLAUSTRUM_GITSTUB_MS2", strconv.FormatInt(delay.Milliseconds(), 10))
	old := callerDeadline
	t.Cleanup(func() { callerDeadline = old })
	var deadline time.Time
	callerDeadline = func(base context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		ctx, cancel := old(base, d)
		deadline, _ = ctx.Deadline()
		return ctx, cancel
	}
	start := time.Now()
	raw, took := f.create(t, s, "w1", "", markerTimeoutMs)
	f.wantSettledCreate(t, raw)
	if took < delay {
		t.Fatalf("the create took %v, want at least the delay %v of the git stub", took, delay)
	}
	if deadline.IsZero() {
		t.Fatal("the create made no deadline")
	}
	if got, limit := deadline.Sub(start), time.Duration(markerTimeoutMs)*time.Millisecond; got > limit+delay/2 {
		t.Errorf("the deadline ends %v after the request, want %v: the slow call moved it", got, limit)
	}
}

// A wait for the create lock of the repository does not count toward timeoutMs:
// the deadline starts once the create holds the lock (claustrum's own, not
// measured). Mutation: take arrival in gitWorktreeCreate before
// withWorktreeCreateLock.
func TestWorktreeCreateDeadlineStartsAfterTheLock(t *testing.T) {
	const wait = 1500 * time.Millisecond
	f, s, _ := settleFixture(t, 0, 0)
	old := callerDeadline
	t.Cleanup(func() { callerDeadline = old })
	var mu sync.Mutex
	var deadline time.Time
	callerDeadline = func(base context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		ctx, cancel := old(base, d)
		mu.Lock()
		deadline, _ = ctx.Deadline()
		mu.Unlock()
		return ctx, cancel
	}
	held, release := make(chan struct{}), make(chan struct{})
	lockDone := make(chan struct{})
	go func() {
		defer close(lockDone)
		withWorktreeCreateLock(f.base, func() response {
			close(held)
			<-release
			return response{}
		})
	}()
	<-held
	var released time.Time
	stop := time.AfterFunc(wait, func() {
		mu.Lock()
		released = time.Now()
		mu.Unlock()
		close(release)
	})
	defer stop.Stop()
	raw, _ := f.create(t, s, "w1", "", markerTimeoutMs)
	<-lockDone
	f.wantSettledCreate(t, raw)
	mu.Lock()
	defer mu.Unlock()
	if deadline.IsZero() || released.IsZero() {
		t.Fatalf("deadline %v, release %v: want both", deadline, released)
	}
	if got, limit := deadline.Sub(released), time.Duration(markerTimeoutMs)*time.Millisecond; got < limit {
		t.Errorf("the deadline ends %v after the lock was free, want at least %v: the wait counted", got, limit)
	}
}

// A slow --attr-source probe before the settle call does not move its stop: the
// limit is read after the probe, so the call stops the margin before the deadline
// (claustrum's own). Mutation: call settleLimit before attrSourceArgs in
// settleCheckout.
func TestSettleCheckoutReadsTheLimitAfterTheProbe(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}
	const probe = time.Second
	installGitSlowStub(t, realGit)
	clearAttrSource()
	t.Cleanup(clearAttrSource)
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "version")
	t.Setenv("CLAUSTRUM_GITSTUB_MS2", strconv.FormatInt(probe.Milliseconds(), 10))
	slowGit(t, "diff-files", "fail", 30*time.Second, "", "")
	setDuration(t, &settleMargin, time.Second)
	setDuration(t, &settleMinLimit, 10*time.Millisecond)
	dir := t.TempDir()
	start := time.Now()
	deadline := start.Add(probe + 1500*time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	settleCheckout(ctx, gitUse{}, dir, dir, filepath.Join(dir, "g"), filepath.Join(dir, "index"), nil, start)
	took := time.Since(start)
	attrSourceMu.Lock()
	ok := attrSourceOK
	attrSourceMu.Unlock()
	if !ok {
		t.Skip("the host git has no --attr-source: no settle call runs")
	}
	if took < probe {
		t.Fatalf("settleCheckout took %v, want at least the probe delay %v", took, probe)
	}
	if want := deadline.Add(-settleMargin).Sub(start); took > want+600*time.Millisecond {
		t.Errorf("the call stopped %v after the start, want about %v: the probe moved the stop", took, want)
	}
}
