package supervisor

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
)

// TestUnheldTTL pins the times of an accepted unattendedSeconds (87375cc7, measured
// on Linux, macOS and Windows): 60 s up to 60, then the value, and 168 h from 604800 on.
func TestUnheldTTL(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{1, "1m0s"}, {5, "1m0s"}, {60, "1m0s"}, {61, "1m1s"}, {240, "4m0s"},
		{604799, "167h59m59s"}, {604800, "168h0m0s"}, {1000000000000, "168h0m0s"},
	}
	for _, c := range cases {
		if got := UnheldTTL(c.n).String(); got != c.want {
			t.Errorf("UnheldTTL(%d) = %s, want %s", c.n, got, c.want)
		}
	}
}

// unheldTicks is a check of an armed child that the test drives. steps holds the step
// that the check asked for. Each send on ticks is one step at that time. stopped
// closes when the check stops its steps, at its return.
type unheldTicks struct {
	steps    chan time.Duration
	ticks    chan time.Time
	stopped  chan struct{}
	stopOnce sync.Once
}

// fakeUnheldTicker puts unheldTicks in place of the ticker of the check until the test
// ends.
func fakeUnheldTicker(t *testing.T) *unheldTicks {
	t.Helper()
	f := &unheldTicks{steps: make(chan time.Duration, 4), ticks: make(chan time.Time), stopped: make(chan struct{})}
	old := unheldTicker
	unheldTicker = func(d time.Duration) (<-chan time.Time, func()) {
		f.steps <- d
		return f.ticks, func() { f.stopOnce.Do(func() { close(f.stopped) }) }
	}
	t.Cleanup(func() { unheldTicker = old })
	return f
}

// step hands the check one step at now, and waits until the check took it.
func (f *unheldTicks) step(t *testing.T, now time.Time) {
	t.Helper()
	select {
	case f.ticks <- now:
	case <-time.After(5 * time.Second):
		t.Fatal("the check took no step in 5 s")
	}
}

// settle hands the check one step at now, then a step at the zero time, which ends
// nothing. The second send returns once the check is done with the first.
func (f *unheldTicks) settle(t *testing.T, now time.Time) {
	t.Helper()
	f.step(t, now)
	f.step(t, time.Time{})
}

// waitStopped waits until the check returned and stopped its steps. A signal of the
// check comes before that return.
func (f *unheldTicks) waitStopped(t *testing.T) {
	t.Helper()
	select {
	case <-f.stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("the check did not return in 30 s")
	}
}

// spawnUnheld starts a sleeping helper armed with ttl on c, and ends it with the test.
func spawnUnheld(t *testing.T, m *Manager, c *conn.Conn, id string, ttl time.Duration) *Proc {
	t.Helper()
	exe, env := helperCommand(t, "sleep")
	p, err := m.SpawnWithTempRoot(c, id, exe, []string{"60"}, "", env, false, nil, 0, TempRootAsk{}, nil, ttl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { endOwnChild(t, p) })
	return p
}

// unheldSinceOf is the clock start of p.
func unheldSinceOf(p *Proc) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.unheldSince
}

// TestUnheldArmLine: the arm line comes right after the start line, with the mapped
// time (87375cc7, measured on Linux, macOS and Windows). The step of the check is a quarter
// of the time (measured: 61 ends at 76.25 s). The cut of the id is claustrum's own.
func TestUnheldArmLine(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeUnheldTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	p := spawnUnheld(t, m, c, "unheld-arm-1", UnheldTTL(61))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantLinesInOrder(t, buf.String(),
		fmt.Sprintf("[process.Manager] Process unheld-a… started, PID=%d, command=%s", p.pid, exe),
		"[process.Manager] Process unheld-a… is to be ended once no connection has been attached to it for 1m1s")
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
	f := fakeUnheldTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	spawnUnheld(t, m, c, "unheld-n0", 0)
	if strings.Contains(buf.String(), "Process unheld-n0 is to be ended") {
		t.Errorf("arm line for a spawn with no time:\n%s", buf.String())
	}
	select {
	case d := <-f.steps:
		t.Errorf("a check with step %s started for a spawn with no time", d)
	default:
	}
}

// TestUnheldClockStartsAtTheDetach: while the connection is attached no step ends the
// child. Its detach starts the clock, and the end comes at the first step with the
// whole time since then (87375cc7, measured on Linux, macOS and Windows).
func TestUnheldClockStartsAtTheDetach(t *testing.T) {
	f := fakeUnheldTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	ttl := time.Minute
	p := spawnUnheld(t, m, c, "unheld-c1", ttl)
	<-f.steps
	signals := countSignalsTo(t, p)
	f.settle(t, time.Now().Add(time.Hour))
	if n := signals.Load(); n != 0 {
		t.Fatalf("%d signals while the connection is attached", n)
	}
	m.DetachConn(c)
	since := unheldSinceOf(p)
	if since.IsZero() {
		t.Fatal("the detach started no clock")
	}
	f.settle(t, since.Add(ttl-time.Millisecond))
	if n := signals.Load(); n != 0 {
		t.Fatalf("%d signals before the whole time", n)
	}
	f.step(t, since.Add(ttl))
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the child did not end at the step after the time")
	}
	if got := p.KilledByForTest(); got != "unattended" {
		t.Errorf("killedBy = %q, want unattended", got)
	}
}

// TestUnheldReattachClearsTheClock: a reattach from an open connection clears the clock,
// and the next detach starts it again (87375cc7, measured on Linux, macOS and Windows).
func TestUnheldReattachClearsTheClock(t *testing.T) {
	f := fakeUnheldTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	p := spawnUnheld(t, m, c, "unheld-r1", time.Minute)
	<-f.steps
	m.DetachConn(c)
	first := unheldSinceOf(p)
	c2, _ := pipeConn(t)
	if _, found, _, _, _, _ := m.Reattach(c2, "unheld-r1", 0); !found {
		t.Fatal("reattach found nothing")
	}
	if s := unheldSinceOf(p); !s.IsZero() {
		t.Fatalf("clock = %s after a reattach, want none", s)
	}
	signals := countSignalsTo(t, p)
	f.settle(t, first.Add(time.Hour))
	if n := signals.Load(); n != 0 {
		t.Fatalf("%d signals while the reattached connection is attached", n)
	}
	// The clock was clear above, so a stamp here comes from the second detach. Two
	// stamps can be equal on a coarse clock (Windows), so the test is "not before".
	m.DetachConn(c2)
	if s := unheldSinceOf(p); s.IsZero() || s.Before(first) {
		t.Errorf("clock = %s after the second detach, want a stamp not before %s", s, first)
	}
}

// TestUnheldClockOfAGoneConnection: a spawn on a connection that is closed already, and
// a reattach on one, start the clock (claustrum's own: their DetachConn can have run).
func TestUnheldClockOfAGoneConnection(t *testing.T) {
	f := fakeUnheldTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	p := spawnUnheld(t, m, closedConn(c), "unheld-g1", time.Minute)
	<-f.steps
	if unheldSinceOf(p).IsZero() {
		t.Error("a spawn on a closed connection started no clock")
	}
	c2, _ := pipeConn(t)
	m.Reattach(c2, "unheld-g1", 0)
	c3, _ := pipeConn(t)
	m.Reattach(closedConn(c3), "unheld-g1", 0)
	if unheldSinceOf(p).IsZero() {
		t.Error("a reattach on a closed connection started no clock")
	}
}

// TestUnheldNoEndAfterTheShutdownBegan: once the shutdown began, a step past the time
// ends nothing (claustrum's own: the shutdown sweep ends the child).
func TestUnheldNoEndAfterTheShutdownBegan(t *testing.T) {
	f := fakeUnheldTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	p := spawnUnheld(t, m, c, "unheld-s1", time.Minute)
	<-f.steps
	m.DetachConn(c)
	signals := countSignalsTo(t, p)
	m.RefuseStarts()
	f.step(t, unheldSinceOf(p).Add(time.Hour))
	f.waitStopped(t)
	if n := signals.Load(); n != 0 {
		t.Errorf("%d signals after the shutdown began", n)
	}
}

// TestUnheldNoLineAfterAnOwnExit: an armed child that exits by itself gets no end line
// (87375cc7, measured on Linux, macOS and Windows).
func TestUnheldNoLineAfterAnOwnExit(t *testing.T) {
	buf := captureLogBuf(t)
	fakeUnheldTicker(t)
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

// TestUnheldClockAtAFailedWrite: a frame that cannot reach the one connection takes it
// off, and that starts the clock of an armed process (claustrum's own).
func TestUnheldClockAtAFailedWrite(t *testing.T) {
	client, server := net.Pipe()
	_ = server.Close()
	_ = client.Close()
	c := conn.New(client, nil, 0, nil, nil, nil)
	p := &Proc{id: "al-w", subs: map[*conn.Conn]struct{}{c: {}}, unheldTTL: time.Minute}
	p.emit(ChildFrame{Stream: "stdout", Data: "a"})
	if unheldSinceOf(p).IsZero() {
		t.Error("a failed write started no clock")
	}
}

// setUnheldBeforeEnd puts f in the seam right before the end until the test ends. It is
// set before the spawn, which reads it.
func setUnheldBeforeEnd(t *testing.T, f func(*Proc)) {
	t.Helper()
	old := unheldBeforeEnd
	unheldBeforeEnd = f
	t.Cleanup(func() { unheldBeforeEnd = old })
}

// TestUnheldNoTermWhenTheShutdownBeginsAtTheStep: a shutdown that begins after the step
// found the whole time, and before the SIGTERM, ends nothing (claustrum's own).
func TestUnheldNoTermWhenTheShutdownBeginsAtTheStep(t *testing.T) {
	f := fakeUnheldTicker(t)
	m := newTestProcManager(t)
	setUnheldBeforeEnd(t, func(*Proc) { m.RefuseStarts() })
	c, _ := pipeConn(t)
	p := spawnUnheld(t, m, c, "al-sd", time.Minute)
	<-f.steps
	m.DetachConn(c)
	signals := countSignalsTo(t, p)
	f.step(t, unheldSinceOf(p).Add(time.Hour))
	f.waitStopped(t)
	if n := signals.Load(); n != 0 {
		t.Errorf("%d signals after the shutdown began", n)
	}
}

// TestUnheldNoTermAfterAReattachAtTheStep: a reattach after the step found the whole
// time, and before the SIGTERM, ends nothing, and the check goes on (claustrum's own).
func TestUnheldNoTermAfterAReattachAtTheStep(t *testing.T) {
	f := fakeUnheldTicker(t)
	m := newTestProcManager(t)
	c2, _ := pipeConn(t)
	setUnheldBeforeEnd(t, func(p *Proc) { m.Reattach(c2, p.id, 0) })
	c, _ := pipeConn(t)
	p := spawnUnheld(t, m, c, "al-ra", time.Minute)
	<-f.steps
	m.DetachConn(c)
	signals := countSignalsTo(t, p)
	f.settle(t, unheldSinceOf(p).Add(time.Hour))
	if n := signals.Load(); n != 0 {
		t.Errorf("%d signals after a reattach at the step", n)
	}
}

// TestUnheldNoTermThroughAClosedGate: once the gate of the end is closed, as after the
// exit of the child is seen, the end sends nothing and logs nothing (claustrum's own).
func TestUnheldNoTermThroughAClosedGate(t *testing.T) {
	buf := captureLogBuf(t)
	f := fakeUnheldTicker(t)
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	p := spawnUnheld(t, m, c, "al-gate", time.Minute)
	<-f.steps
	m.DetachConn(c)
	p.unheldGate.Close()
	signals := countSignalsTo(t, p)
	f.step(t, unheldSinceOf(p).Add(time.Hour))
	f.waitStopped(t)
	if n := signals.Load(); n != 0 {
		t.Errorf("%d signals through a closed gate", n)
	}
	p.mu.Lock()
	termed := p.unheldTermed
	p.mu.Unlock()
	if termed || strings.Contains(buf.String(), "Process al-gate: no connection attached") {
		t.Errorf("the end counts a SIGTERM through a closed gate:\n%s", buf.String())
	}
}
