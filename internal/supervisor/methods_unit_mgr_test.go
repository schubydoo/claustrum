package supervisor

import (
	"testing"

	"github.com/schubydoo/claustrum/internal/conn"
)

// The Manager mutators must be no-ops (not panics) for unknown ids and
// procs that never got a running command/stdin.
func TestProcManagerEdgeCases(t *testing.T) {
	m := newTestProcManager(t)

	if m.WriteStdin("missing", []byte("x")) {
		t.Error("writeStdin(missing) = true, want false")
	}
	// A proc with no stdin pipe rejects writes without panicking.
	m.procs["nostdin"] = &Proc{id: "nostdin", subs: map[*conn.Conn]struct{}{}}
	if m.WriteStdin("nostdin", []byte("x")) {
		t.Error("writeStdin(no stdin) = true, want false")
	}

	// kill on an unknown id and on a proc with no started process are both no-ops.
	_ = m.Kill("missing", "TERM")
	_ = m.Kill("nostdin", "KILL")

	// killAll over procs that never started must not panic either.
	m.KillAll()
}

// DetachConn unsubscribes a conn from every managed proc's replay fan-out.
func TestProcManagerDetachConn(t *testing.T) {
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	p := &Proc{id: "p", subs: map[*conn.Conn]struct{}{c: {}}}
	m.procs["p"] = p

	m.DetachConn(c)
	p.mu.Lock()
	_, still := p.subs[c]
	p.mu.Unlock()
	if still {
		t.Error("detachConn left the conn subscribed")
	}
}
