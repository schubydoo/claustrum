package main

import (
	"strings"
	"testing"
	"time"
)

// writeJSON surfaces a marshal failure instead of writing a broken frame; the
// error must come back before any bytes touch the connection (nc stays nil —
// reaching a write would panic).
func TestWriteJSONMarshalError(t *testing.T) {
	c := &conn{}
	if err := c.writeJSON(map[string]any{"bad": make(chan int)}); err == nil {
		t.Error("writeJSON of an unmarshalable value succeeded, want error")
	}
}

// serveConn skips blank request lines (a client sending "\n" keepalives or a
// trailing newline after a frame) without dispatching or replying, and the
// connection keeps serving afterwards.
func TestServeConnSkipsEmptyLines(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.send("") // a bare newline: no reply, no closed connection
	cl.send("")
	reply := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`)))
	if !strings.Contains(reply, `"pong":true`) {
		t.Fatalf("ping after empty lines = %s, want pong:true", reply)
	}
}

// startMetricsServer propagates an unbindable address as an error (runServe
// logs it and keeps going — the socket, not the metrics endpoint, is the job).
func TestStartMetricsServerBadAddr(t *testing.T) {
	if ln, err := startMetricsServer("127.0.0.1:99999999"); err == nil {
		_ = ln.Close()
		t.Error("startMetricsServer on an invalid port succeeded, want error")
	}
}

// The backoff in acceptLoop must stop doubling at the 1s ceiling: after enough
// consecutive failures (5ms·2^n crosses 1s on the 9th), each retry waits the
// capped 1s instead of growing without bound. Slow by construction (~2.3s of
// deliberate backoff sleeps), so skipped under -short.
func TestAcceptLoopBackoffCapsAtOneSecond(t *testing.T) {
	if testing.Short() {
		t.Skip("waits through ~2.3s of real backoff; skipped under -short")
	}
	ln := &erroringListener{}
	s := &server{
		procs:    newProcManager(),
		conns:    make(map[*conn]struct{}),
		shutdown: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() { s.acceptLoop(ln); close(done) }()

	// The 9th Accept failure is the first whose doubled delay (1.28s) trips the
	// cap; the preceding sleeps sum to ~1.28s.
	deadline := time.Now().Add(10 * time.Second)
	for {
		ln.mu.Lock()
		n := ln.calls
		ln.mu.Unlock()
		if n >= 9 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d Accept calls before deadline, want >= 9", n)
		}
		time.Sleep(20 * time.Millisecond)
	}

	s.signalShutdown()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("acceptLoop did not return after shutdown")
	}
}
