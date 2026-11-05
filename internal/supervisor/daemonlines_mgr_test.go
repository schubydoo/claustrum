package supervisor

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestExitLineOfAKilledChild: the exit line of a child that process.kill ended carries
// ", asked to end by this daemon (client)". That is the text of build 52d82f0a, measured on
// Linux and macOS. c8e2f083 printed ", signalled by this daemon (client)". 89cb6289 printed ", signalled at client request" there, measured on
// Windows in all 10 kill rows and on Linux. A child that ends by itself gets no such tail.
func TestExitLineOfAKilledChild(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, _ := pipeConn(t)
	exe, env := helperCommand(t, "sleep")
	killed, err := m.Spawn(c, "k1", exe, []string{"60"}, "", env, true)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	buf := captureLogBuf(t)
	_ = m.Kill("k1", "KILL")
	echo, envEcho := helperCommand(t, "echo")
	plain, err := m.Spawn(c, "p1", echo, []string{"hi"}, "", envEcho, true)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	for _, p := range []*Proc{killed, plain} {
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("process %s did not end", p.id)
		}
	}
	log := buf.String()
	if !regexp.MustCompile(`\[process\.Manager\] Process k1 exited with code -?\d+(, terminated by SIGKILL)?, asked to end by this daemon \(client\)\n`).MatchString(log) {
		t.Errorf("log lacks the exit line of the killed child with its tail:\n%s", log)
	}
	if !strings.Contains(log, "[process.Manager] Process p1 exited with code 0\n") {
		t.Errorf("log lacks the plain exit line of the child that ended by itself:\n%s", log)
	}
}

// TestDrainGraceLine: a grandchild holds the stdout of a child that has ended. When the
// drain grace is over, the daemon logs the reference's line and then closes the pipes.
// Measured on Linux and on Windows against 89cb6289 (the rows with kids that inherit
// the stdout and stderr of the child).
func TestDrainGraceLine(t *testing.T) {
	oldDrain := exitDrainGrace
	exitDrainGrace = 200 * time.Millisecond
	t.Cleanup(func() { exitDrainGrace = oldDrain })
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, _ := pipeConn(t)
	exe, env := helperCommand(t, "orphan-stdout")
	buf := captureLogBuf(t)
	p, err := m.Spawn(c, "d1", exe, []string{"3"}, "", env, true) // the grandchild holds stdout for 3 s
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("no exit of the managed process")
	}
	wantLinesInOrder(t, buf.String(),
		"[process.Manager] Process d1: pipe drain grace expired (grandchild holding stdio?); force-closing",
		"[process.Manager] Process d1 exited with code 7")
}
