package git

import (
	"bufio"
	"context"
	"io"
	"os"
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

// TestContentCheckDeadlineKillsTwoHelpers: the deadline passes while one helper
// of the check is held. The `cat-file --batch` and a running commit hash-object
// get one kill each (4c111e86 and 87375cc7, measured on Linux). The tree
// hash-object gets none, a limit of this change. No stop line comes. Mutations:
// no fire in the watch (no kill), and a track for every kind (the tree call is
// killed).
func TestContentCheckDeadlineKillsTwoHelpers(t *testing.T) {
	const batch, commitHash, treeHash = "cat-file\x1f--batch", "commit\x1f--literally", "tree\x1f--literally"
	for _, tc := range []struct {
		name, hold string
		want       []string
	}{
		{"during the batch", "cat-file,--batch", []string{batch}},
		{"during the commit hash", "hash-object,commit,--stdin-paths", []string{batch, commitHash}},
		{"during the tree hash", "hash-object,tree,--stdin-paths", []string{batch, commitHash}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".mcp.json": "{}\n"})
			gate := gateStub(t, tc.hold, 30*time.Second, "", "")
			timeLog := filepath.Join(t.TempDir(), "times")
			t.Setenv("CLAUSTRUM_GITSTUB_TIMELOG", timeLog)
			t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o644) })
			killed := recordHelperKills(t)
			// The gate opens after the deadline, so a held call that gets no
			// kill ends too.
			whileHeld := make(chan int, 1)
			firedDeadlineThen(t, func() bool { return exists(gate + ".in") }, func() {
				for end := time.Now().Add(5 * time.Second); len(killed()) < len(tc.want) && time.Now().Before(end); {
					time.Sleep(5 * time.Millisecond)
				}
				whileHeld <- len(killed())
				_ = os.WriteFile(gate, nil, 0o644)
			})
			logs := captureLogBuf(t)
			raw, _ := f.create(t, map[string]any{"timeoutMs": markerTimeoutMs})
			if !strings.Contains(raw, `"errorCode":"timeout"`) {
				t.Fatalf("git.worktree_create = %s, want the timeout of a good checkout", raw)
			}
			if n := <-whileHeld; n != len(tc.want) {
				t.Errorf("%d kills while the call was held, want %d", n, len(tc.want))
			}
			var want []int
			for _, m := range tc.want {
				want = append(want, stubPids(t, timeLog, m)...)
			}
			got := killed()
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("killed pids %v, want %v", got, want)
			}
			for _, pid := range stubPids(t, timeLog, treeHash) {
				if slices.Contains(got, pid) {
					t.Errorf("the tree hash-object %d got a kill", pid)
				}
			}
			if strings.Contains(logs.String(), "stopped using the long-running git") {
				t.Errorf("log:\n%s\nwant no stop line", logs)
			}
		})
	}
}

// TestContentCheckNoDeadlineNoKill: a create with no timeoutMs, and one whose
// deadline does not pass during the check, send no kill (4c111e86 and 87375cc7,
// measured on Linux). Mutation: a fire in stopBatch with no test of the deadline.
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

// fakeKillBatch is a batch on pipes with no process, and a helper with a fake
// handle. sawOpen reports whether stdin of the batch was open at each kill.
func fakeKillBatch(t *testing.T) (*objectBatch, *helperKills, func() []bool) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inR.Close(); _ = inW.Close() })
	outR, outW := io.Pipe()
	t.Cleanup(func() { _ = outW.Close() })
	b := &objectBatch{in: inW, inR: inR, outR: outR, out: bufio.NewReader(outR), done: make(chan struct{})}
	kills := &helperKills{helpers: []*deadlineHelper{{p: &os.Process{}}}}
	var mu sync.Mutex
	var open []bool
	old := deadlineHelperKill
	t.Cleanup(func() { deadlineHelperKill = old })
	deadlineHelperKill = func(*os.Process) error {
		_, err := inW.Stat()
		mu.Lock()
		open = append(open, err == nil)
		mu.Unlock()
		return nil
	}
	return b, kills, func() []bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(open)
	}
}

// TestDeadlineKillComesBeforeStdinCloses: both paths that close stdin at the
// deadline send the kill first (measured on Linux). Mutations: the drop before
// the fire in readAtDeadline, and in stopBatch.
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

// TestHelperKillsSendsOnceAndNotAfterTheGate: a second fire sends nothing, a
// closed gate sends nothing, and a helper that starts after the fire is not
// tracked. Mutations: no fired test in fire, and no gate around the kill.
func TestHelperKillsSendsOnceAndNotAfterTheGate(t *testing.T) {
	calls := 0
	old := deadlineHelperKill
	t.Cleanup(func() { deadlineHelperKill = old })
	deadlineHelperKill = func(*os.Process) error { calls++; return nil }
	live, reaped := &deadlineHelper{p: &os.Process{}}, &deadlineHelper{p: &os.Process{}}
	reaped.gate.Close()
	k := &helperKills{helpers: []*deadlineHelper{live, reaped}}
	k.fire()
	k.fire()
	if calls != 1 {
		t.Errorf("%d kills, want 1 for the live helper alone", calls)
	}
	k.track(&os.Process{Pid: -1})
	if len(k.helpers) != 2 {
		t.Errorf("a helper was tracked after the fire: %d helpers", len(k.helpers))
	}
	var none *helperKills
	none.fire()
	if none.sent() {
		t.Error("a nil helperKills reports a sent kill")
	}
}
