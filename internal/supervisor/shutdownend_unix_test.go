//go:build unix

package supervisor

import (
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestShutdownSweepKillsWhatOutlivesTheGrace pins the end of the wait of b3081b51
// (measured on Linux and macOS): two stream-json children that ignore SIGTERM get the
// TERM, one timer, and then the group SIGKILL. The KillAll line counts them as killed.
// The wait is shortened here. Its length of 1.5 s is the reference's.
func TestShutdownSweepKillsWhatOutlivesTheGrace(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	old := shutdownEndGrace
	shutdownEndGrace = 300 * time.Millisecond
	t.Cleanup(func() { shutdownEndGrace = old })
	exe, env := helperCommand(t, "ignore-term")
	var procs []*Proc
	for _, tc := range []struct {
		id   string
		args []string
	}{
		{"c1", []string{"--input-format=stream-json"}},
		{"c2", []string{"--output-format", "stream-json"}},
	} {
		c, frames := pipeConn(t)
		p, err := m.Spawn(c, tc.id, exe, tc.args, "", env, false)
		if err != nil {
			t.Fatalf("spawn %s: %v", tc.id, err)
		}
		// "ready" comes after the helper ignores SIGTERM.
		select {
		case f := <-frames:
			if f.Stream != "stdout" {
				t.Fatalf("first frame of %s is %q, want the ready line on stdout", tc.id, f.Stream)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("the helper %s never became ready", tc.id)
		}
		procs = append(procs, p)
	}
	sent := recordShutdownSignals(t, false, procs...)

	start := time.Now()
	if n := m.KillAllCount(); n != 2 {
		t.Errorf("killAllCount = %d, want 2", n)
	}
	if d := time.Since(start); d < shutdownEndGrace {
		t.Errorf("the sweep returned after %v, want the wait of %v first", d, shutdownEndGrace)
	}
	for _, p := range procs {
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("the child %s outlived the shutdown sweep", p.id)
		}
		if got, want := sent(p), []string{"TERM", "KILL"}; !slices.Equal(got, want) {
			t.Errorf("signals to %s = %q, want %q", p.id, got, want)
		}
	}
	const line = "[process.Manager] KillAll: asked 2 Claude Code process(es) to end: 0 exited, 2 had to be killed, after "
	if !strings.Contains(logs.String(), line) {
		t.Errorf("log = %q\nwant it to hold %q", logs.String(), line)
	}
}

// TestShutdownSweepKillsAGrandchildThatHoldsThePipes: the stream-json child ends on
// SIGTERM, and its grandchild in its group holds its stdout. The exit frame waits, so
// the end of the wait sends the group SIGKILL, and that ends the grandchild (b3081b51
// sends that SIGKILL, measured on Linux and macOS. The grandchild case is claustrum's
// own, not measured). The group is the test's own while the grandchild lives.
func TestShutdownSweepKillsAGrandchildThatHoldsThePipes(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	old := shutdownEndGrace
	shutdownEndGrace = 300 * time.Millisecond
	t.Cleanup(func() { shutdownEndGrace = old })
	pidFile := filepath.Join(t.TempDir(), "gpid")
	exe, env := helperCommand(t, "tree-stdout")
	c, _ := pipeConn(t)
	p, err := m.Spawn(c, "c1", exe, []string{pidFile, "stream-json"}, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	gpid := waitPIDFile(t, pidFile)
	// The test ends its own grandchild only while it can still be alive: after it is
	// gone, its pid can name another process.
	gone := false
	t.Cleanup(func() {
		if !gone {
			_ = syscall.Kill(gpid, syscall.SIGKILL)
		}
	})
	sent := recordShutdownSignals(t, true, p)

	if n := m.KillAllCount(); n != 1 {
		t.Errorf("killAllCount = %d, want 1", n)
	}
	if gone = waitProcessGone(gpid, 5*time.Second); !gone {
		t.Errorf("the grandchild %d outlived the shutdown sweep", gpid)
	}
	if got, want := sent(p), []string{"TERM", "KILL"}; !slices.Equal(got, want) {
		t.Errorf("signals to the child = %q, want %q", got, want)
	}
	const line = "[process.Manager] KillAll: asked 1 Claude Code process(es) to end: 0 exited, 1 had to be killed, after "
	if !strings.Contains(logs.String(), line) {
		t.Errorf("log = %q\nwant it to hold %q", logs.String(), line)
	}
}
