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
// closed gate sends nothing, and a helper that comes after the fire is not
// tracked. The end of the check sends the one repeat on Linux and macOS, and none
// on Windows. Mutations: no fired test in fire, no gate around the send, a track
// after the fire, and no repeat at the end.
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
	k.track(&os.Process{Pid: -1}, nil)
	if len(k.helpers) != 2 {
		t.Errorf("a helper was tracked after the fire: %d helpers", len(k.helpers))
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

// TestHelperAfterTheKillLosesItsPipes: a helper that comes after the kills went
// out gets no kill, and its pipes close, so a read of the check does not wait for
// it. A helper that comes after the end of a check with no kill keeps its pipes.
// Mutation: no drop in the early return of track.
func TestHelperAfterTheKillLosesItsPipes(t *testing.T) {
	setRepeatAfter(t, time.Hour)
	calls := countHelperKills(t)
	k := newHelperKills()
	t.Cleanup(k.release)
	k.fire()
	drops := 0
	k.track(&os.Process{Pid: -1}, func() { drops++ })
	if drops != 1 {
		t.Errorf("%d drops of a helper after the kill, want 1", drops)
	}
	if n := calls(); n != 0 {
		t.Errorf("%d kills of a helper after the kill, want none", n)
	}
	k.track(&os.Process{Pid: -1}, nil)
	ended := newHelperKills()
	ended.release()
	drops = 0
	ended.track(&os.Process{Pid: -1}, func() { drops++ })
	if drops != 0 {
		t.Errorf("%d drops after the end of a check with no kill, want none", drops)
	}
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
