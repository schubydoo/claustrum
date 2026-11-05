package main

import (
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/supervisor"
)

// TestArgsAskEnd pins the args that make the shutdown sweep ask a child to end
// first: "stream-json" as one arg, "--input-format=stream-json", and the two args
// "--output-format" "stream-json" (b3081b51, measured on Linux and macOS), and the
// form "--output-format=stream-json" (b3081b51, measured on Linux).
func TestArgsAskEnd(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"stream-json"}, true},
		{[]string{"--input-format=stream-json"}, true},
		{[]string{"--output-format", "stream-json"}, true},
		{[]string{"--output-format=stream-json"}, true},
		{[]string{"status", "stream-json"}, true},
		{nil, false},
		{[]string{"60"}, false},
		{[]string{"--input-format=text", "stream-jsonx", "xstream-json"}, false},
	} {
		if got := argsAskEnd(tc.args); got != tc.want {
			t.Errorf("argsAskEnd(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// recordShutdownSignals records the names of the signals that go to the given children
// through the signalGroup seam, until the test ends. Every signal still reaches the
// child, which is a child of the test. With groupKill false a KILL on Linux and macOS
// goes through the handle of the child, never to a group: after the exit of the child
// its group number is no longer the test's own. A test whose own grandchild holds the
// group passes true.
func recordShutdownSignals(t *testing.T, groupKill bool, procs ...*managedProc) func(p *managedProc) []string {
	t.Helper()
	realSignal := signalGroup
	var mu sync.Mutex
	sent := map[*os.Process][]string{}
	signalGroup = func(g *supervisor.ProcGroup, proc *os.Process, signame string) error {
		for _, p := range procs {
			if proc == p.cmd.Process {
				mu.Lock()
				sent[proc] = append(sent[proc], signame)
				mu.Unlock()
				if signame == "KILL" && supervisor.ShutdownAsksEnd && !groupKill {
					_ = proc.Kill()
					return nil
				}
				return realSignal(g, proc, signame)
			}
		}
		return realSignal(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = realSignal })
	return func(p *managedProc) []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(sent[p.cmd.Process])
	}
}

// TestShutdownSweepSignalsByArgs pins the shutdown sweep of b3081b51. On Linux and
// macOS a child whose args ask for stream-json gets SIGTERM first and ends by it. The
// sweep then logs the KillAll line. A child with other args gets the group SIGKILL at
// once (measured on Linux and macOS). After the exit frame the ended child gets no
// group SIGKILL (claustrum's own). On Windows every child gets the kill at once, and
// no KillAll line comes (measured on Windows).
func TestShutdownSweepSignalsByArgs(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	sleep, env := helperCommand(t, "sleep")
	ca, framesA := pipeConn(t)
	asked, err := m.spawn(ca, "asked", sleep, []string{"60", "stream-json"}, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	cb, _ := pipeConn(t)
	plain, err := m.spawn(cb, "plain", sleep, []string{"60"}, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	sent := recordShutdownSignals(t, false, asked, plain)

	if n := m.killAllCount(); n != 2 {
		t.Errorf("killAllCount = %d, want 2", n)
	}
	for _, p := range []*managedProc{asked, plain} {
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("the child %s did not end on the shutdown sweep", p.id)
		}
	}
	wantAsked := []string{"KILL"}
	if supervisor.ShutdownAsksEnd {
		wantAsked = []string{"TERM"}
	}
	if got := sent(asked); !slices.Equal(got, wantAsked) {
		t.Errorf("signals to the stream-json child = %q, want %q", got, wantAsked)
	}
	if got := sent(plain); !slices.Equal(got, []string{"KILL"}) {
		t.Errorf("signals to the other child = %q, want %q", got, []string{"KILL"})
	}
	// The exit frame names the signal that ended the child: SIGTERM, not a KILL that
	// came before the exit. Windows names no signal.
	wantSig := ""
	if supervisor.ShutdownAsksEnd {
		wantSig = "SIGTERM"
	}
	if got := exitSignalOf(t, framesA); got != wantSig {
		t.Errorf("exit frame signal of the stream-json child = %q, want %q", got, wantSig)
	}
	const line = "[process.Manager] KillAll: asked 1 Claude Code process(es) to end: 1 exited, 0 had to be killed, after "
	if got := strings.Contains(logs.String(), line); got != supervisor.ShutdownAsksEnd {
		t.Errorf("log holds %q = %v, want %v\nlog: %q", line, got, supervisor.ShutdownAsksEnd, logs.String())
	}
}

// TestShutdownSweepWithNoStreamJSONChildDoesNotWait pins the sweep with no child that
// argsAskEnd accepts: no KillAll line and no wait (b3081b51, measured on Linux and
// macOS).
func TestShutdownSweepWithNoStreamJSONChildDoesNotWait(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	old := shutdownEndGrace
	shutdownEndGrace = 30 * time.Second
	t.Cleanup(func() { shutdownEndGrace = old })
	sleep, env := helperCommand(t, "sleep")
	c, _ := pipeConn(t)
	p, err := m.spawn(c, "plain", sleep, []string{"60"}, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	start := time.Now()
	if n := m.killAllCount(); n != 1 {
		t.Errorf("killAllCount = %d, want 1", n)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the sweep took %v, want no wait", d)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the child did not end on the shutdown sweep")
	}
	if strings.Contains(logs.String(), "KillAll:") {
		t.Errorf("log holds a KillAll line, want none: %q", logs.String())
	}
}

// exitSignalOf reads frames until the exit frame and returns its signal member.
func exitSignalOf(t *testing.T, frames <-chan streamFrame) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f := <-frames:
			if f.Stream == "exit" {
				return f.Signal
			}
		case <-deadline:
			t.Fatal("no exit frame")
			return ""
		}
	}
}

// TestShutdownWaitSendsNoKillAfterRunningTurnsFalse pins the guard of the end of the
// shutdown wait (D28): a child whose running flag is false gets no group SIGKILL,
// also while its done channel is still open. The test builds that state by hand from
// its own ended child, and the seam delivers nothing.
func TestShutdownWaitSendsNoKillAfterRunningTurnsFalse(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	old := shutdownEndGrace
	shutdownEndGrace = 10 * time.Millisecond
	t.Cleanup(func() { shutdownEndGrace = old })
	exe, env := helperCommand(t, "echo")
	c, _ := pipeConn(t)
	ended, err := m.spawn(c, "e1", exe, []string{"x"}, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	select {
	case <-ended.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the helper did not end")
	}
	q := &managedProc{id: "q", cmd: ended.cmd, group: ended.group, running: false, reaped: true, done: make(chan struct{})}
	realSignal := signalGroup
	var mu sync.Mutex
	var sent []string
	signalGroup = func(_ *supervisor.ProcGroup, _ *os.Process, signame string) error {
		mu.Lock()
		sent = append(sent, signame)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { signalGroup = realSignal })
	logs := captureLogBuf(t)
	endAsked([]*managedProc{q}, time.Now())
	mu.Lock()
	got := slices.Clone(sent)
	mu.Unlock()
	if len(got) != 0 {
		t.Errorf("signals after running turned false = %q, want none", got)
	}
	const line = "KillAll: asked 1 Claude Code process(es) to end: 0 exited, 1 had to be killed, after "
	if !strings.Contains(logs.String(), line) {
		t.Errorf("log = %q\nwant it to hold %q", logs.String(), line)
	}
}
