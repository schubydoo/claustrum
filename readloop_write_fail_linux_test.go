package main

import (
	"net"
	"testing"
	"time"
)

// These tests are Linux only. The rule was measured on Linux, and the tests
// need a failed reply write: on Linux the shutdown of the read side of an
// AF_UNIX socket makes the peer's writes fail with EPIPE. Other systems are
// not known to do that.

// dialReadShut dials sock and shuts the read side, so a reply write of the
// daemon to this client fails. It returns the client once the daemon holds it.
func dialReadShut(t *testing.T, s *server, sock string) *net.UnixConn {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { nc.Close() })
	uc := nc.(*net.UnixConn)
	if err := uc.CloseRead(); err != nil {
		t.Fatalf("CloseRead: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.connCount() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("daemon never registered the connection")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return uc
}

// A failed write of a -32001 or -32700 reply ends the read loop of that
// connection (c8e2f083, Linux).
func TestFailedWriteOfReadLoopErrorEndsConnection(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"unauthorized", `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"wrong"}`},
		{"parse error", `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sock := newRunningServer(t)
			uc := dialReadShut(t, s, sock)
			// The client may see EPIPE once the daemon closed. Ignore write errors.
			_, _ = uc.Write([]byte(tc.line + "\n"))
			deadline := time.Now().Add(5 * time.Second)
			for s.connCount() != 0 {
				if time.Now().After(deadline) {
					t.Fatal("connection stayed after a failed write of the error reply")
				}
				_, _ = uc.Write([]byte(tc.line + "\n"))
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

// Control: a failed write of a reply that the request goroutine writes keeps
// the connection. The count must stay 1 for a whole second, so a slow close
// cannot pass the test by luck.
func TestFailedWriteOfRequestReplyKeepsConnection(t *testing.T) {
	s, sock := newRunningServer(t)
	uc := dialReadShut(t, s, sock)
	for i := 0; i < 3; i++ {
		if _, err := uc.Write([]byte(authed(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`) + "\n")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		if n := s.connCount(); n != 1 {
			t.Fatalf("connections after failed request-reply writes = %d, want 1", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
