package main

import (
	"strings"
	"testing"
)

// The cleanup of the harness waits for the server side of each connection. So
// the close line of a connection is in the log when the test that made it ends,
// and no later test can see it.
func TestHarnessCleanupWaitsForTheServerSideOfItsConnections(t *testing.T) {
	buf := captureLogBuf(t)
	var s *server
	t.Run("inner", func(t *testing.T) {
		var sock string
		s, sock = newRunningServer(t)
		// Many connections, so that a cleanup with no wait leaves one open more often.
		for range 20 {
			dial(t, sock).call(authed(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`))
		}
	})
	s.mu.Lock()
	open := len(s.conns)
	s.mu.Unlock()
	if open != 0 {
		t.Errorf("%d server connections still open after the cleanup of the test that made them", open)
	}
	if n := strings.Count(buf.String(), "[Server] Connection closed: "); n < 20 {
		t.Errorf("close lines at the end of the inner test = %d, want at least 20", n)
	}
}
