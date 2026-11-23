package git

import (
	"bufio"
	"context"
	"errors"
	"io"
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

// recordHelperKills replaces deadlineHelperKill. Each kill is recorded and sent
// to the child of this test. pids returns the killed pids in order.
func recordHelperKills(t *testing.T) (pids func() []int) {
	t.Helper()
	var mu sync.Mutex
	var got []int
	old := deadlineHelperKill
	t.Cleanup(func() { deadlineHelperKill = old })
	deadlineHelperKill = func(p *os.Process) error {
		mu.Lock()
		got = append(got, p.Pid)
		mu.Unlock()
		return p.Kill()
	}
	return func() []int {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// sortedPids is pids sorted, with each pid once.
func sortedPids(pids []int) []int {
	out := slices.Clone(pids)
	slices.Sort(out)
	return slices.Compact(out)
}

// stubPids is the pids of the stub calls in timeLog whose argv holds match.
func stubPids(t *testing.T, timeLog, match string) []int {
	t.Helper()
	b, err := os.ReadFile(timeLog)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) > 3 && f[0] == "start" && strings.Contains(line, match) {
			pid, err := strconv.Atoi(f[2])
			if err != nil {
				t.Fatal(err)
			}
			pids = append(pids, pid)
		}
	}
	return pids
}

// TestContentCheckDeadlineKillsTheHelpers: the deadline passes while one helper
// of the check is held for 30 s. Each running helper gets the kill, and the reply
// does not wait for the held call (87375cc7, measured on Linux and Windows, and on
// macOS for three kinds). No stop line comes. Mutations: no fire in the watch, no
// track of the long-running calls, no track of the file hash, and a wait for the
// file hash after the kill.
func TestContentCheckDeadlineKillsTheHelpers(t *testing.T) {
	const batch, commitHash, treeHash, fileHash = "cat-file\x1f--batch", "commit\x1f--literally", "tree\x1f--literally", "hash-object\x1f--no-filters"
	for _, tc := range []struct {
		name, hold string
		fileOnly   bool
		want       []string
	}{
		{"during the batch", "cat-file,--batch", false, []string{batch}},
		{"during the commit hash", "hash-object,commit,--stdin-paths", false, []string{batch, commitHash}},
		{"during the tree hash", "hash-object,tree,--stdin-paths", false, []string{batch, commitHash, treeHash}},
		{"during the file hash", "hash-object,--stdin-paths", true, []string{batch, commitHash, treeHash, fileHash}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".mcp.json": "{}\n"})
			gate := gateStub(t, tc.hold, 30*time.Second, "", "")
			if tc.fileOnly {
				contentTestCallOnly(t)
			}
			timeLog := filepath.Join(t.TempDir(), "times")
			t.Setenv("CLAUSTRUM_GITSTUB_TIMELOG", timeLog)
			t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o644) })
			killed := recordHelperKills(t)
			firedDeadlineAt(t, func() bool { return exists(gate + ".in") })
			logs := captureLogBuf(t)
			begin := time.Now()
			raw, _ := f.create(t, map[string]any{"timeoutMs": markerTimeoutMs})
			if took := time.Since(begin); took > 20*time.Second {
				t.Errorf("the create took %v, want an answer that does not wait for the held call", took)
			}
			if !strings.Contains(raw, `"errorCode":"timeout"`) {
				t.Fatalf("git.worktree_create = %s, want the timeout of a good checkout", raw)
			}
			var want []int
			for _, m := range tc.want {
				want = append(want, stubPids(t, timeLog, m)...)
			}
			if got, want := sortedPids(killed()), sortedPids(want); !slices.Equal(got, want) {
				t.Errorf("killed pids %v, want %v", got, want)
			}
			if strings.Contains(logs.String(), "stopped using the long-running git") {
				t.Errorf("log:\n%s\nwant no stop line", logs)
			}
		})
	}
}

// TestContentCheckNoDeadlineNoKill: a create with no timeoutMs, and one whose
// deadline does not pass during the check, send no kill (87375cc7, measured on
// Linux and macOS). Mutation: a fire in stopBatch with no test of the deadline.
func TestContentCheckNoDeadlineNoKill(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]any
	}{
		{"no deadline", nil},
		{"far deadline", map[string]any{"timeoutMs": markerTimeoutMs}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".mcp.json": "{}\n"})
			killed := recordHelperKills(t)
			raw, _ := f.create(t, tc.extra)
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s, want success", raw)
			}
			if got := killed(); len(got) != 0 {
				t.Errorf("killed pids %v, want none", got)
			}
		})
	}
}

// setRepeatAfter sets helperRepeatAfter for the test.
func setRepeatAfter(t *testing.T, d time.Duration) {
	t.Helper()
	old := helperRepeatAfter
	t.Cleanup(func() { helperRepeatAfter = old })
	helperRepeatAfter = d
}

// fakeKillBatch is a batch on pipes with no process, and a helper with a fake
// handle whose drop closes the batch. open reports whether stdin of the batch was
// open at each kill. A write of no bytes tells a closed file on every system.
func fakeKillBatch(t *testing.T) (*objectBatch, *helperKills, func() []bool) {
	t.Helper()
	setRepeatAfter(t, time.Hour)
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inR.Close(); _ = inW.Close() })
	outR, outW := io.Pipe()
	t.Cleanup(func() { _ = outW.Close() })
	b := &objectBatch{in: inW, inR: inR, outR: outR, out: bufio.NewReader(outR), done: make(chan struct{})}
	kills := newHelperKills()
	kills.helpers = []*deadlineHelper{{p: &os.Process{}, drop: b.drop}}
	var mu sync.Mutex
	var open []bool
	old := deadlineHelperKill
	t.Cleanup(func() { deadlineHelperKill = old })
	deadlineHelperKill = func(*os.Process) error {
		_, err := inW.Write(nil)
		mu.Lock()
		open = append(open, !errors.Is(err, os.ErrClosed))
		mu.Unlock()
		return nil
	}
	t.Cleanup(kills.release)
	return b, kills, func() []bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(open)
	}
}

// TestDeadlineKillComesBeforeStdinCloses: the paths that close stdin at the
// deadline send the kill first (87375cc7, measured on Linux). Mutations: the drop
// before the send in fire, in readAtDeadline, and in stopBatch.
func TestDeadlineKillComesBeforeStdinCloses(t *testing.T) {
	t.Run("readAtDeadline", func(t *testing.T) {
		b, kills, open := fakeKillBatch(t)
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		c := &contentCheck{deadline: ctx, kills: kills}
		time.AfterFunc(10*time.Millisecond, func() { cancel(errCallerTimeoutMs) })
		if _, _, err := c.readAtDeadline(b, "commit", strings.Repeat("a", 40)); err == nil {
			t.Fatal("the read at the deadline gave no error")
		}
		if got := open(); !slices.Equal(got, []bool{true}) {
			t.Errorf("stdin open at each kill = %v, want one kill with stdin open", got)
		}
	})
	t.Run("stopBatch", func(t *testing.T) {
		b, kills, open := fakeKillBatch(t)
		c := &contentCheck{batch: b, kills: kills, atDeadline: true}
		c.stopBatch()
		if got := open(); !slices.Equal(got, []bool{true}) {
			t.Errorf("stdin open at each kill = %v, want one kill with stdin open", got)
		}
	})
	t.Run("fire", func(t *testing.T) {
		b, kills, open := fakeKillBatch(t)
		kills.fire()
		if got := open(); !slices.Equal(got, []bool{true}) {
			t.Errorf("stdin open at each kill = %v, want one kill with stdin open", got)
		}
		if _, err := b.in.Write(nil); !errors.Is(err, os.ErrClosed) {
			t.Errorf("stdin after the kill: %v, want it closed", err)
		}
	})
}

// TestDeadlineKillWaitsOutTheKeep: the time of the LFS keep does not count, so
// the watch fires only after it. A sent kill counts as the deadline of the
// check. Mutations: no waitForKeep in the watch, and no sent test in
// pastDeadline.
func TestDeadlineKillWaitsOutTheKeep(t *testing.T) {
	const keep = 400 * time.Millisecond
	base, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	at := time.Now()
	ctx := firedDeadlineCtx{base, func() time.Time { return at }}
	c := &contentCheck{u: gitUse{stop: &gitStop{}}, deadline: ctx, keepTook: keep}
	defer c.armDeadlineKill()()
	cancel(errCallerTimeoutMs)
	for time.Since(at) < keep/2 {
		if c.kills.sent() {
			t.Fatalf("the kill went out %v after the deadline, inside the keep of %v", time.Since(at), keep)
		}
		time.Sleep(5 * time.Millisecond)
	}
	for end := time.Now().Add(10 * time.Second); !c.kills.sent(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("no kill after the keep")
		}
	}
	open := &contentCheck{deadline: context.Background(), kills: &helperKills{fired: true}}
	if !open.pastDeadline() {
		t.Error("a sent kill does not count as the deadline")
	}
}

// countHelperKills replaces deadlineHelperKill with a count.
func countHelperKills(t *testing.T) (count func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	old := deadlineHelperKill
	t.Cleanup(func() { deadlineHelperKill = old })
	deadlineHelperKill = func(*os.Process) error {
		mu.Lock()
		n++
		mu.Unlock()
		return nil
	}
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// TestHelperKillsSendsOnceAndNotAfterTheGate: a second fire sends nothing, a
// closed gate sends nothing. The end of the check sends the one repeat on Linux and macOS, and none
// on Windows. Mutations: no fired test in fire, no gate around the send, and no
// repeat at the end.
func TestHelperKillsSendsOnceAndNotAfterTheGate(t *testing.T) {
	setRepeatAfter(t, time.Hour)
	calls := countHelperKills(t)
	live, reaped := &deadlineHelper{p: &os.Process{}}, &deadlineHelper{p: &os.Process{}}
	reaped.gate.Close()
	k := newHelperKills()
	k.helpers = []*deadlineHelper{live, reaped}
	k.fire()
	k.fire()
	if n := calls(); n != 1 {
		t.Errorf("%d kills, want 1 for the live helper alone", n)
	}
	k.release()
	want := 1
	if holdKilledHelpers {
		want = 2
	}
	if n := calls(); n != want {
		t.Errorf("%d kills after the end of the check, want %d", n, want)
	}
	var none *helperKills
	none.fire()
	if none.sent() {
		t.Error("a nil helperKills reports a sent kill")
	}
}

// startExitChild starts a child that ends at the end of its stdin. The cleanup
// ends it and reaps it, so no child outlives the test.
func startExitChild(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperExitChild$")
	cmd.Env = append(os.Environ(), "CLAUSTRUM_HELPER_EXIT_CHILD=1", "CLAUSTRUM_TEST_HELPER=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// TestHelperAfterTheKillGetsItsKill: a helper that registers after the kills went
// out joins the helpers and gets its kill through its gate and then its drop. The
// exit wait covers it. A helper after the end of a check with no kill gets neither.
// Mutations: no join after the fire, no late kill, the drop before the kill, and
// no late drop.
func TestHelperAfterTheKillGetsItsKill(t *testing.T) {
	setRepeatAfter(t, time.Hour)
	var mu sync.Mutex
	var order []string
	old := deadlineHelperKill
	t.Cleanup(func() { deadlineHelperKill = old })
	deadlineHelperKill = func(p *os.Process) error {
		mu.Lock()
		order = append(order, "kill")
		mu.Unlock()
		return p.Kill()
	}
	cmd := startExitChild(t)
	k := newHelperKills()
	release := sync.OnceFunc(k.release)
	t.Cleanup(release)
	k.fire()
	tracked := make(chan struct{})
	go func() {
		k.track(cmd.Process, func() {
			mu.Lock()
			order = append(order, "drop")
			mu.Unlock()
		})
		close(tracked)
	}()
	gone := make(chan struct{})
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		k.mu.Lock()
		n := len(k.helpers)
		k.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the helper did not join after the kill")
		}
	}
	go func() {
		k.awaitKilledExits(30 * time.Second)
		close(gone)
	}()
	select {
	case <-gone:
	case <-time.After(20 * time.Second):
		t.Fatal("the late helper did not end")
	}
	release()
	select {
	case <-tracked:
	case <-time.After(10 * time.Second):
		t.Fatal("the track of the late helper did not end")
	}
	mu.Lock()
	got := slices.Clone(order)
	mu.Unlock()
	if !slices.Equal(got, []string{"kill", "drop"}) {
		t.Errorf("order = %q, want one kill and then the drop", got)
	}
	ended := newHelperKills()
	ended.release()
	drops := 0
	ended.track(&os.Process{Pid: -1}, func() { drops++ })
	if drops != 0 || len(ended.helpers) != 0 {
		t.Errorf("after the end of a check with no kill: %d drops, %d helpers", drops, len(ended.helpers))
	}
}

// TestExitWaitCountsAStartUnderWay: the wait at the end of the check waits for a
// reserved start, inside its bound, and the clear counts once. Mutations: no wait
// for the starts, no bound on it, and no close at a count of 0.
func TestExitWaitCountsAStartUnderWay(t *testing.T) {
	setRepeatAfter(t, time.Hour)
	countHelperKills(t)
	k := newHelperKills()
	defer k.release()
	first, second := k.reserve(), k.reserve()
	k.fire()
	ended := make(chan struct{})
	go func() {
		k.awaitKilledExits(time.Hour)
		close(ended)
	}()
	first()
	first()
	select {
	case <-ended:
		t.Fatal("the wait ended with a start still under way")
	case <-time.After(50 * time.Millisecond):
	}
	second()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the wait did not end after the last start")
	}
	stuck := newHelperKills()
	defer stuck.release()
	stuck.reserve()
	stuck.fire()
	bounded := make(chan struct{})
	go func() {
		stuck.awaitKilledExits(10 * time.Millisecond)
		close(bounded)
	}()
	select {
	case <-bounded:
	case <-time.After(10 * time.Second):
		t.Fatal("the wait for a start has no bound")
	}
}

// lateStartRow runs a create whose deadline fires after the last deadline test of
// one helper and before its start, while the call waits for the hold of its
// repository folder. That helper does not start, so only the want helpers that
// run already get a kill (4c111e86 and 87375cc7, measured on Linux for a deadline
// during its listing).
func lateStartRow(t *testing.T, hold string, fileOnly bool, isCall func([]string) bool, want int) {
	t.Helper()
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".mcp.json": "{}\n"})
	gate := gateStub(t, hold, 10*time.Second, "", "")
	if fileOnly {
		contentTestCallOnly(t)
	}
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o644) })
	killed := recordHelperKills(t)
	var reached atomic.Bool
	fired := make(chan struct{})
	old := beforeRepoHold
	t.Cleanup(func() { beforeRepoHold = old })
	beforeRepoHold = func(args []string) {
		if !isCall(args) || reached.Swap(true) {
			return
		}
		select {
		case <-fired:
			for end := time.Now().Add(5 * time.Second); len(sortedPids(killed())) < want && time.Now().Before(end); {
				time.Sleep(time.Millisecond)
			}
		case <-time.After(10 * time.Second):
			t.Error("the deadline did not fire")
		}
	}
	firedDeadlineThen(t, reached.Load, func() { close(fired) })
	raw, _ := f.create(t, map[string]any{"timeoutMs": markerTimeoutMs})
	if !reached.Load() {
		t.Fatalf("the seam did not run for the call, reply %s", raw)
	}
	if !strings.Contains(raw, `"errorCode":"timeout"`) {
		t.Fatalf("git.worktree_create = %s, want the timeout of a good checkout", raw)
	}
	if got := sortedPids(killed()); len(got) != want {
		t.Errorf("killed pids %v, want %d: a helper started after the kill", got, want)
	}
}

// TestFileHashDoesNotStartAfterTheKill: lateStartRow for the file hash. Mutations:
// no beforeStart test, and none for the file hash.
func TestFileHashDoesNotStartAfterTheKill(t *testing.T) {
	lateStartRow(t, "hash-object,--stdin-paths", true, func(args []string) bool {
		return slices.Contains(args, "--no-filters") && slices.Contains(args, "--stdin-paths") && !slices.Contains(args, "--literally")
	}, 3)
}

// TestCommitHashDoesNotStartAfterTheKill: lateStartRow for the commit hash-object.
// Mutation: no beforeStart for the long-running calls.
func TestCommitHashDoesNotStartAfterTheKill(t *testing.T) {
	lateStartRow(t, "hash-object,commit,--stdin-paths", false, func(args []string) bool {
		return slices.Contains(args, "commit") && slices.Contains(args, "--literally")
	}, 1)
}

// TestLateKillGoesThroughTheGate: a late helper whose gate is closed gets no send.
// Mutation: the late kill outside the gate.
func TestLateKillGoesThroughTheGate(t *testing.T) {
	setRepeatAfter(t, time.Hour)
	calls := countHelperKills(t)
	old := helperExitWait
	t.Cleanup(func() { helperExitWait = old })
	helperExitWait = func(*os.Process) bool { return true }
	k := newHelperKills()
	defer k.release()
	k.fire()
	h := &deadlineHelper{p: &os.Process{Pid: -1}, exited: make(chan struct{})}
	h.gate.Close()
	k.runTracked(h, nil)
	if n := calls(); n != 0 {
		t.Errorf("%d kills through a closed gate, want none", n)
	}
}

// TestJoinClearsTheStartCount: a helper that joins clears its count of reserve
// while it still runs. Mutation: no clear after the join.
func TestJoinClearsTheStartCount(t *testing.T) {
	release := make(chan struct{})
	old := helperExitWait
	t.Cleanup(func() { helperExitWait = old })
	helperExitWait = func(*os.Process) bool { <-release; return true }
	k := newHelperKills()
	defer k.release()
	unreserve := k.reserve()
	ended := make(chan struct{})
	go func() {
		k.runTracked(&deadlineHelper{p: &os.Process{Pid: -1}, exited: make(chan struct{})}, unreserve)
		close(ended)
	}()
	defer func() {
		close(release)
		<-ended
	}()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		k.mu.Lock()
		n := k.starting
		k.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(end) {
			t.Fatal("the count of the start did not clear at the join")
		}
	}
}

// TestUnseenExitIsNotAnExit: a wait that did not see the exit leaves the helper
// counted as running. Mutation: exited closes on an unseen exit.
func TestUnseenExitIsNotAnExit(t *testing.T) {
	old := helperExitWait
	t.Cleanup(func() { helperExitWait = old })
	helperExitWait = func(*os.Process) bool { return false }
	k := newHelperKills()
	defer k.release()
	// A child of this test that is already reaped: the gate has no running process
	// to wait for after the unseen exit.
	cmd := startExitChild(t)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	h := &deadlineHelper{p: cmd.Process, exited: make(chan struct{})}
	k.runTracked(h, nil)
	select {
	case <-h.exited:
		t.Error("a wait that failed marked the helper as gone")
	default:
	}
}

// TestAwaitKilledExits: after the kill the wait ends with the exit of each helper,
// or at its bound. With no kill it returns at once. Mutations: no fired test, no
// wait for a helper, and no bound.
func TestAwaitKilledExits(t *testing.T) {
	setRepeatAfter(t, time.Hour)
	countHelperKills(t)
	wait := func(k *helperKills, bound time.Duration) chan struct{} {
		ended := make(chan struct{})
		go func() {
			k.awaitKilledExits(bound)
			close(ended)
		}()
		return ended
	}
	t.Run("no kill", func(t *testing.T) {
		k := newHelperKills()
		defer k.release()
		k.helpers = []*deadlineHelper{{p: &os.Process{}, exited: make(chan struct{})}}
		select {
		case <-wait(k, time.Hour):
		case <-time.After(10 * time.Second):
			t.Fatal("the wait ran with no kill")
		}
	})
	t.Run("ends with the exits", func(t *testing.T) {
		a, b := make(chan struct{}), make(chan struct{})
		k := newHelperKills()
		defer k.release()
		k.helpers = []*deadlineHelper{{p: &os.Process{}, exited: a}, {p: &os.Process{}, exited: b}}
		k.fire()
		ended := wait(k, time.Hour)
		close(a)
		select {
		case <-ended:
			t.Fatal("the wait ended with one helper still running")
		case <-time.After(50 * time.Millisecond):
		}
		close(b)
		select {
		case <-ended:
		case <-time.After(10 * time.Second):
			t.Fatal("the wait did not end after the exits")
		}
	})
	t.Run("ends at the bound", func(t *testing.T) {
		k := newHelperKills()
		defer k.release()
		k.helpers = []*deadlineHelper{{p: &os.Process{}, exited: make(chan struct{})}}
		k.fire()
		select {
		case <-wait(k, 10*time.Millisecond):
		case <-time.After(10 * time.Second):
			t.Fatal("the wait has no bound")
		}
	})
}

// TestCheckEndWaitsForKilledExits: with the wait on, the stop of the watch returns
// only after the exit of a killed helper. With it off, as on Linux and macOS, it
// does not wait. Mutation: no call of the wait in the stop.
func TestCheckEndWaitsForKilledExits(t *testing.T) {
	setRepeatAfter(t, time.Hour)
	countHelperKills(t)
	old := awaitExitsAtEnd
	t.Cleanup(func() { awaitExitsAtEnd = old })
	for _, on := range []bool{true, false} {
		awaitExitsAtEnd = on
		ctx, cancel := context.WithCancelCause(context.Background())
		c := &contentCheck{u: gitUse{stop: &gitStop{}}, deadline: ctx}
		stop := c.armDeadlineKill()
		exited := make(chan struct{})
		c.kills.helpers = []*deadlineHelper{{p: &os.Process{}, exited: exited}}
		cancel(errCallerTimeoutMs)
		for end := time.Now().Add(10 * time.Second); !c.kills.sent(); time.Sleep(time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("no kill")
			}
		}
		stopped := make(chan struct{})
		go func() {
			stop()
			close(stopped)
		}()
		select {
		case <-stopped:
			if on {
				t.Error("the end of the check did not wait for the killed helper")
			}
		case <-time.After(time.Second):
			if !on {
				t.Error("the end of the check waited with the wait off")
			}
		}
		close(exited)
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			t.Fatal("the end of the check did not finish after the exit")
		}
	}
}

// TestTrackMarksTheExit: a tracked helper marks its exit, also while its reap
// waits for the end of the check. Mutation: no close of exited.
func TestTrackMarksTheExit(t *testing.T) {
	setRepeatAfter(t, time.Hour)
	recordHelperKills(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperExitChild$")
	cmd.Env = append(os.Environ(), "CLAUSTRUM_HELPER_EXIT_CHILD=1", "CLAUSTRUM_TEST_HELPER=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	k := newHelperKills()
	tracked := make(chan struct{})
	go func() {
		k.track(cmd.Process, nil)
		close(tracked)
	}()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		k.mu.Lock()
		n := len(k.helpers)
		k.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(end) {
			_ = cmd.Process.Kill()
			t.Fatal("the helper was not tracked")
		}
	}
	k.fire()
	gone := make(chan struct{})
	go func() {
		k.awaitKilledExits(time.Hour)
		close(gone)
	}()
	select {
	case <-gone:
	case <-time.After(10 * time.Second):
		t.Error("the exit of the killed helper was not marked")
	}
	k.release()
	<-tracked
	_ = cmd.Wait()
}

// TestFileHashGetsNoRepeat: the file hash gets one send, and another helper gets
// its repeat at the end of the check on Linux and macOS (87375cc7, measured).
// Mutation: no once test in sendRepeat.
func TestFileHashGetsNoRepeat(t *testing.T) {
	setRepeatAfter(t, time.Hour)
	var mu sync.Mutex
	sent := map[*os.Process]int{}
	old := deadlineHelperKill
	t.Cleanup(func() { deadlineHelperKill = old })
	deadlineHelperKill = func(p *os.Process) error {
		mu.Lock()
		sent[p]++
		mu.Unlock()
		return nil
	}
	file, batch := &os.Process{}, &os.Process{}
	k := newHelperKills()
	k.helpers = []*deadlineHelper{{p: file, once: true}, {p: batch}}
	k.fire()
	k.release()
	want := 1
	if holdKilledHelpers {
		want = 2
	}
	if sent[file] != 1 || sent[batch] != want {
		t.Errorf("sends: file hash %d, other helper %d, want 1 and %d", sent[file], sent[batch], want)
	}
}

// TestAwaitFileHash: the wait ends with the call, and after the kill at the end of
// the grace. With no grace it ends at the kill. Mutations: no wait for the grace,
// and no bound on it.
func TestAwaitFileHash(t *testing.T) {
	type outcome struct {
		r  fileHashEnd
		ok bool
	}
	run := func(ended chan fileHashEnd, fired chan struct{}, grace time.Duration) chan outcome {
		got := make(chan outcome, 1)
		go func() {
			r, ok := awaitFileHash(ended, fired, grace)
			got <- outcome{r, ok}
		}()
		return got
	}
	t.Run("ends with the call after the kill", func(t *testing.T) {
		ended, fired := make(chan fileHashEnd, 1), make(chan struct{})
		close(fired)
		got := run(ended, fired, time.Hour)
		select {
		case o := <-got:
			t.Fatalf("the wait ended at the kill: %+v", o)
		case <-time.After(50 * time.Millisecond):
		}
		ended <- fileHashEnd{out: []byte("x")}
		select {
		case o := <-got:
			if !o.ok || string(o.r.out) != "x" {
				t.Errorf("result %+v, want the call", o)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the wait did not end with the call")
		}
	})
	t.Run("ends at the grace", func(t *testing.T) {
		fired := make(chan struct{})
		close(fired)
		select {
		case o := <-run(make(chan fileHashEnd), fired, 10*time.Millisecond):
			if o.ok {
				t.Errorf("result %+v, want no result", o)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the wait has no bound")
		}
	})
	t.Run("no grace", func(t *testing.T) {
		fired := make(chan struct{})
		close(fired)
		select {
		case o := <-run(make(chan fileHashEnd), fired, 0):
			if o.ok {
				t.Errorf("result %+v, want no result", o)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the wait did not end at the kill")
		}
	})
}

// TestHelperKillsNoKillAfterTheEnd: a deadline that fires after the end of the
// check sends nothing. Mutation: no ended test in fire.
func TestHelperKillsNoKillAfterTheEnd(t *testing.T) {
	calls := countHelperKills(t)
	k := newHelperKills()
	k.helpers = []*deadlineHelper{{p: &os.Process{}}}
	k.release()
	k.fire()
	if n := calls(); n != 0 {
		t.Errorf("%d kills after the end of the check, want none", n)
	}
}

// TestEndLongDoesNotWaitAfterTheKill: after the kill the check does not wait for
// a helper, whose reap waits for the end of the check. Mutation: endLong always
// waits for the end of git.
func TestEndLongDoesNotWaitAfterTheKill(t *testing.T) {
	b, kills, _ := fakeKillBatch(t)
	kills.fire()
	c := &contentCheck{kills: kills}
	ended := make(chan struct{})
	go func() {
		c.endLong(b)
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		close(b.done)
		t.Fatal("endLong waited for a killed helper")
	}
}

// TestHelperExitChild is the child of TestAwaitHelperExitWaitsForTheExit. It
// ends at the end of its stdin.
func TestHelperExitChild(t *testing.T) {
	if os.Getenv("CLAUSTRUM_HELPER_EXIT_CHILD") == "" {
		t.Skip("a child of TestAwaitHelperExitWaitsForTheExit")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// TestAwaitHelperExitWaitsForTheExit: the wait returns only after the exit of the
// child, on each system, so the gate of a helper stays open while it runs.
// Mutation: a wait that returns at once.
func TestAwaitHelperExitWaitsForTheExit(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperExitChild$")
	cmd.Env = append(os.Environ(), "CLAUSTRUM_HELPER_EXIT_CHILD=1", "CLAUSTRUM_TEST_HELPER=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	seen := make(chan bool, 1)
	go func() { seen <- awaitHelperExit(cmd.Process) }()
	select {
	case <-seen:
		t.Error("the wait returned while the child runs")
	case <-time.After(200 * time.Millisecond):
	}
	_ = stdin.Close()
	select {
	case ok := <-seen:
		if !ok {
			t.Error("the wait did not see the exit")
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Error("the wait did not end after the exit")
	}
	_ = cmd.Wait()
}
