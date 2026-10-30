package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
// while a "shadowDirOff":true request runs.
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
