package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAcceptLoopServesAndReturnsOnShutdown drives the PRODUCTION
// (*server).acceptLoop directly. The socket harness (newRunningServer) inlines
// its own copy of the accept loop so it can skip the daemonize/os.Exit shell —
// which left the real acceptLoop, its connection registration, and its
// shutdown-return branch uncovered. This exercises all three: a live connection
// is accepted and served (a ping round-trips through acceptLoop -> serveConn ->
// dispatch), the conn is registered in s.conns, and closing the listener while
// shutdown is signalled makes acceptLoop return via its <-s.shutdown branch
// instead of logging and retrying on the accept error.
func TestAcceptLoopServesAndReturnsOnShutdown(t *testing.T) {
	// Short socket path (macOS sun_path limit), mirroring newRunningServer.
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	s := &server{
		token:    testToken,
		ln:       ln,
		procs:    newProcManager(),
		conns:    make(map[*conn]struct{}),
		shutdown: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() { s.acceptLoop(s.ln); close(done) }()
	t.Cleanup(func() { s.procs.killAll() })

	// A ping must round-trip through the real acceptLoop.
	cl := dial(t, sock)
	reply := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`)))
	if !strings.Contains(reply, `"pong":true`) {
		t.Fatalf("ping reply = %s, want pong:true", reply)
	}

	// acceptLoop must have registered the accepted connection.
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.conns)
		s.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("acceptLoop registered %d conns, want 1", n)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Under shutdown, closing the listener must make acceptLoop return (the
	// Accept error is discriminated as shutdown, not a retryable error).
	s.signalShutdown()
	_ = ln.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("acceptLoop did not return after shutdown + listener close")
	}
}
