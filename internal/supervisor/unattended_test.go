package supervisor

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
)

// TestAloneTTL pins the times of an accepted unattendedSeconds (87375cc7, measured
// on macOS and Windows): 60 s up to 60, then the value, and 168 h from 604800 on.
func TestAloneTTL(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{1, "1m0s"}, {5, "1m0s"}, {60, "1m0s"}, {61, "1m1s"}, {240, "4m0s"},
		{604799, "167h59m59s"}, {604800, "168h0m0s"}, {1000000000000, "168h0m0s"},
	}
	for _, c := range cases {
		if got := AloneTTL(c.n).String(); got != c.want {
			t.Errorf("AloneTTL(%d) = %s, want %s", c.n, got, c.want)
		}
	}
}

// aloneTicks is a check of an armed child that the test drives. steps holds the step
// that the check asked for. Each send on ticks is one step at that time.
type aloneTicks struct {
	steps chan time.Duration
	ticks chan time.Time
}

// fakeAloneTicker puts aloneTicks in place of the ticker of the check until the test
// ends.
func fakeAloneTicker(t *testing.T) *aloneTicks {
	t.Helper()
	f := &aloneTicks{steps: make(chan time.Duration, 4), ticks: make(chan time.Time)}
	old := aloneTicker
	aloneTicker = func(d time.Duration) (<-chan time.Time, func()) {
		f.steps <- d
		return f.ticks, func() {}
	}
	t.Cleanup(func() { aloneTicker = old })
	return f
}

// tick hands the check one step at now, and waits until the check took it.
func (f *aloneTicks) tick(t *testing.T, now time.Time) {
	t.Helper()
	select {
	case f.ticks <- now:
	case <-time.After(5 * time.Second):
		t.Fatal("the check took no step in 5 s")
	}
}

// settle hands the check one step at now, then a step at the zero time, which ends
// nothing. The second send returns once the check is done with the first.
func (f *aloneTicks) settle(t *testing.T, now time.Time) {
	t.Helper()
	f.tick(t, now)
	f.tick(t, time.Time{})
}

// spawnAlone starts a sleeping helper armed with ttl on c, and ends it with the test.
func spawnAlone(t *testing.T, m *Manager, c *conn.Conn, id string, ttl time.Duration) *Proc {
	t.Helper()
	exe, env := helperCommand(t, "sleep")
	p, err := m.SpawnWithTempRoot(c, id, exe, []string{"60"}, "", env, false, nil, 0, TempRootAsk{}, nil, ttl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { endOwnChild(t, p) })
	return p
}

// aloneSinceOf is the clock start of p.
func aloneSinceOf(p *Proc) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.aloneSince
}

// TestAloneArmLine: the arm line comes right after the start line, with the mapped
// time (87375cc7, measured on macOS and Windows). The step of the check is a quarter
// of the time (measured: 61 ends at 76.25 s). The cut of the id is claustrum's own.
func TestAloneArmLine(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeAloneTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	p := spawnAlone(t, m, c, "alone-arm-1", AloneTTL(61))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantLinesInOrder(t, buf.String(),
		fmt.Sprintf("[process.Manager] Process alone-ar… started, PID=%d, command=%s", p.pid, exe),
		"[process.Manager] Process alone-ar… is to be ended once no connection has been attached to it for 1m1s")
	select {
	case d := <-f.steps:
		if d != 15250*time.Millisecond {
			t.Errorf("step = %s, want 15.25s", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the check never started")
	}
}

// TestNoArmLineWithNoTime: a spawn with no time logs no arm line and starts no check.
func TestNoArmLineWithNoTime(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeAloneTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	spawnAlone(t, m, c, "alone-n0", 0)
	if strings.Contains(buf.String(), "Process alone-n0 is to be ended") {
		t.Errorf("arm line for a spawn with no time:\n%s", buf.String())
	}
	select {
	case d := <-f.steps:
		t.Errorf("a check with step %s started for a spawn with no time", d)
	default:
	}
}

// TestAloneClockStartsAtTheDetach: while the connection is attached no step ends the
// child. Its detach starts the clock, and the end comes at the first step with the
// whole time since then (87375cc7, measured on macOS and Windows).
func TestAloneClockStartsAtTheDetach(t *testing.T) {
	f := fakeAloneTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	ttl := time.Minute
	p := spawnAlone(t, m, c, "alone-c1", ttl)
	<-f.steps
	signals := countSignalsTo(t, p)
	f.settle(t, time.Now().Add(time.Hour))
	if n := signals.Load(); n != 0 {
		t.Fatalf("%d signals while the connection is attached", n)
	}
	m.DetachConn(c)
	since := aloneSinceOf(p)
	if since.IsZero() {
		t.Fatal("the detach started no clock")
	}
	f.settle(t, since.Add(ttl-time.Millisecond))
	if n := signals.Load(); n != 0 {
		t.Fatalf("%d signals before the whole time", n)
	}
	f.tick(t, since.Add(ttl))
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the child did not end at the step after the time")
	}
	if got := p.KilledByForTest(); got != "unattended" {
		t.Errorf("killedBy = %q, want unattended", got)
	}
}

// TestAloneReattachClearsTheClock: a reattach from an open connection clears the clock,
// and the next detach starts it again (87375cc7, measured on macOS and Windows).
func TestAloneReattachClearsTheClock(t *testing.T) {
	f := fakeAloneTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	p := spawnAlone(t, m, c, "alone-r1", time.Minute)
	<-f.steps
	m.DetachConn(c)
	first := aloneSinceOf(p)
	c2, _ := pipeConn(t)
	if _, found, _, _, _, _ := m.Reattach(c2, "alone-r1", 0); !found {
		t.Fatal("reattach found nothing")
	}
	if s := aloneSinceOf(p); !s.IsZero() {
		t.Fatalf("clock = %s after a reattach, want none", s)
	}
	signals := countSignalsTo(t, p)
	f.settle(t, first.Add(time.Hour))
	if n := signals.Load(); n != 0 {
		t.Fatalf("%d signals while the reattached connection is attached", n)
	}
	m.DetachConn(c2)
	if s := aloneSinceOf(p); !s.After(first) {
		t.Errorf("clock = %s after the second detach, want after %s", s, first)
	}
}

// TestAloneClockOfAGoneConnection: a spawn on a connection that is closed already, and
// a reattach on one, start the clock (claustrum's own: their DetachConn can have run).
func TestAloneClockOfAGoneConnection(t *testing.T) {
	f := fakeAloneTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	p := spawnAlone(t, m, closedConn(c), "alone-g1", time.Minute)
	<-f.steps
	if aloneSinceOf(p).IsZero() {
		t.Error("a spawn on a closed connection started no clock")
	}
	c2, _ := pipeConn(t)
	m.Reattach(c2, "alone-g1", 0)
	c3, _ := pipeConn(t)
	m.Reattach(closedConn(c3), "alone-g1", 0)
	if aloneSinceOf(p).IsZero() {
		t.Error("a reattach on a closed connection started no clock")
	}
}

// TestAloneNoEndAfterTheShutdownBegan: once the shutdown began, a step past the time
// ends nothing (claustrum's own: the shutdown sweep ends the child).
func TestAloneNoEndAfterTheShutdownBegan(t *testing.T) {
	f := fakeAloneTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	p := spawnAlone(t, m, c, "alone-s1", time.Minute)
	<-f.steps
	m.DetachConn(c)
	signals := countSignalsTo(t, p)
	m.RefuseStarts()
	f.tick(t, aloneSinceOf(p).Add(time.Hour))
	select {
	case f.ticks <- time.Now():
		t.Fatal("the check took another step after the shutdown began")
	case <-time.After(200 * time.Millisecond):
	}
	if n := signals.Load(); n != 0 {
		t.Errorf("%d signals after the shutdown began", n)
	}
}

// TestAloneNoLineAfterAnOwnExit: an armed child that exits by itself gets no end line
// (87375cc7, measured on macOS and Windows).
func TestAloneNoLineAfterAnOwnExit(t *testing.T) {
	buf := captureLogBuf(t)
	fakeAloneTicker(t)
	m := newTestProcManager(t)
	c, frames := pipeConn(t)
	exe, env := helperCommand(t, "echo")
	if _, err := m.SpawnWithTempRoot(c, "al-own", exe, []string{"x"}, "", env, false, nil, 0, TempRootAsk{}, nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	waitExitFrame(t, frames)
	logs := buf.String()
	if !strings.Contains(logs, "[process.Manager] Process al-own exited with code 0\n") {
		t.Fatalf("no exited line:\n%s", logs)
	}
	if strings.Contains(logs, "Process al-own: no connection attached") {
		t.Errorf("an end line after an own exit:\n%s", logs)
	}
}

// TestAloneClockAtAFailedWrite: a frame that cannot reach the one connection takes it
// off, and that starts the clock of an armed process (claustrum's own).
func TestAloneClockAtAFailedWrite(t *testing.T) {
	client, server := net.Pipe()
	_ = server.Close()
	_ = client.Close()
	c := conn.New(client, nil, 0, nil, nil, nil)
	p := &Proc{id: "al-w", subs: map[*conn.Conn]struct{}{c: {}}, aloneTTL: time.Minute}
	p.emit(ChildFrame{Stream: "stdout", Data: "a"})
	if aloneSinceOf(p).IsZero() {
		t.Error("a failed write started no clock")
	}
}
