package main

import (
	"errors"
	"testing"
)

// TestLateSessionSpawnSupersedesNothing: a session spawn that comes after the close of
// the start gate sends no signal to the prior process of its session. That rule is
// claustrum's own and not measured.
func TestLateSessionSpawnSupersedesNothing(t *testing.T) {
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	cat, env := helperCommand(t, "cat")
	first, err := m.spawn(c, "s1", cat, sessionArgs, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { endOwnChild(t, first) })
	signals := countSignalsTo(t, first)
	m.stopStarts()
	if _, err := m.spawn(c, "s2", cat, sessionArgs, "", env, false); !errors.Is(err, errStartsStopped) {
		t.Fatalf("the spawn after the close of the gate answered %v, want errStartsStopped", err)
	}
	// The supersede runs inside the spawn, so the count is final here.
	if n := signals.Load(); n != 0 {
		t.Errorf("the late session spawn sent %d signal(s) to the prior process, want none", n)
	}
	if m.get("s2") != nil {
		t.Error("the late session spawn is in the table")
	}
}
