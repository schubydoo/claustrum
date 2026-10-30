package main

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

// The tests of the connection log rules (claustrum's own, not
// measured). The text and length tests are in log_order_test.go.

func TestLogSampledRule(t *testing.T) {
	for n, want := range map[uint64]bool{
		1: true, 2: true, 10: true, 11: false, 999: false, 1000: true, 1001: false, 2000: true, 2500: false, 3000: true,
	} {
		if got := logSampled(n); got != want {
			t.Errorf("logSampled(%d) = %v, want %v", n, got, want)
		}
	}
}

// countedNumbers returns the counts that the lines with prefix carry, in order.
func countedNumbers(out, prefix string) []int {
	var ns []int
	for _, l := range strings.Split(out, "\n") {
		if i := strings.Index(l, prefix); i >= 0 {
			var n int
			if _, err := fmt.Sscanf(l[i+len(prefix):], "%d", &n); err == nil {
				ns = append(ns, n)
			}
		}
	}
	return ns
}

func wantNumbers(t *testing.T, what string, got []int) {
	t.Helper()
	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 1000}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s lines carry the counts %v, want %v", what, got, want)
	}
}

// 1000 refused requests log the counts 1 to 10 and 1000. A line that is not
// JSON does the same on its own counter, and neither sets the token mark.
func TestRefusedAndParseLinesFollowTheSampleRule(t *testing.T) {
	s := &server{token: testToken}
	c := &conn{}
	out := captureLog(t, func() {
		for i := 0; i < 1000; i++ {
			s.gate(c, []byte(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`))
			s.gate(c, []byte(`{`))
		}
	})
	wantNumbers(t, "Unauthorized", countedNumbers(out, "Unauthorized request number "))
	wantNumbers(t, "Parse error", countedNumbers(out, "Parse error number "))
	if c.tokenMark().Load() {
		t.Error("a refused request or a parse error set the token mark")
	}
	// The counters belong to the daemon: a second connection goes on counting.
	out = captureLog(t, func() { s.gate(&conn{}, []byte(`{`)) })
	if strings.Contains(out, "Parse error number") {
		t.Errorf("count 1001 was logged: %q", out)
	}
}

// The method is cut to 64 characters and quoted, and the id is not logged.
func TestUnauthorizedLineCutsAndQuotesTheMethod(t *testing.T) {
	s := &server{token: testToken}
	m := strings.Repeat("a", 70)
	out := captureLog(t, func() {
		s.gate(nil, []byte(`{"jsonrpc":"2.0","id":7,"method":"`+m+`"}`))
	})
	want := `[Server] Unauthorized request number 1: method="` + strings.Repeat("a", 64) + `"` + "\n"
	if !strings.HasSuffix(out, want) {
		t.Errorf("log = %q, want it to end with %q", out, want)
	}
}

// server.shutdown passes the check with no token, so it sets the mark.
func TestShutdownRequestSetsTheTokenMark(t *testing.T) {
	s := &server{token: testToken}
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	c := &conn{nc: srv}
	out := captureLog(t, func() {
		s.gate(c, []byte(`{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}`))
		s.gate(c, []byte(`{"jsonrpc":"2.0","id":2,"method":"server.shutdown"}`))
	})
	if !c.tokenMark().Load() {
		t.Error("server.shutdown did not set the token mark")
	}
	if n := strings.Count(out, `[Server] New connection from: "pipe"`+"\n"); n != 1 {
		t.Errorf("New connection lines = %d, want 1; log:\n%s", n, out)
	}
}

// The close of a connection with no token mark counts on its own counter.
func TestTokenlessCloseLinesFollowTheSampleRule(t *testing.T) {
	s := &server{token: testToken, procs: newTestProcManager(t), conns: map[*conn]struct{}{}, shutdown: make(chan struct{})}
	out := captureLog(t, func() {
		for i := 0; i < 12; i++ {
			cli, srv := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); s.serveConn(&conn{nc: srv}) }()
			_, _ = cli.Write([]byte("x"))
			_ = cli.Close()
			<-done
		}
	})
	got := countedNumbers(out, "Connection number ")
	if fmt.Sprint(got) != fmt.Sprint([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) {
		t.Errorf("counted close lines carry %v, want 1 to 10", got)
	}
}

// A connection that closes with no byte logs no token-less line and does not raise the count.
func TestTokenlessCloseNeedsAByte(t *testing.T) {
	s := &server{token: testToken, procs: newTestProcManager(t), conns: map[*conn]struct{}{}, shutdown: make(chan struct{})}
	out := captureLog(t, func() {
		for _, send := range []bool{false, true} {
			cli, srv := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); s.serveConn(&conn{nc: srv}) }()
			if send {
				_, _ = cli.Write([]byte("x"))
			}
			_ = cli.Close()
			<-done
		}
	})
	if got := fmt.Sprint(countedNumbers(out, "Connection number ")); got != "[1]" {
		t.Errorf("counted close lines carry %v, want [1] (one for the connection that sent a byte)", got)
	}
}

// A read error on a connection with no token mark logs no scanner error, and one
// during the daemon shutdown logs none either. A marked connection logs it.
func TestScannerErrorLineSkips(t *testing.T) {
	over := func(c *conn, show bool) string {
		s := &server{token: testToken, procs: newTestProcManager(t), conns: map[*conn]struct{}{}, shutdown: make(chan struct{})}
		cli, srv := net.Pipe()
		c.nc = srv
		return captureLog(t, func() {
			done := make(chan struct{})
			go func() { defer close(done); s.serveConn(c) }()
			if show {
				showTokenOn(t, cli)
			}
			_, _ = cli.Write([]byte(strings.Repeat("x", 1024*1024)))
			_ = cli.Close()
			<-done
		})
	}
	if out := over(&conn{}, true); !strings.Contains(out, `[Server] scanner error on "pipe": "bufio.Scanner: token too long"`+"\n") {
		t.Errorf("a marked connection logged no quoted scanner error:\n%s", out)
	}
	if out := over(&conn{}, false); strings.Contains(out, "scanner error") || !strings.Contains(out, "never showed the token") {
		t.Errorf("a connection with no token mark logged a scanner error or no counted close:\n%s", out)
	}
	down := make(chan struct{})
	close(down)
	if out := over(&conn{down: down}, true); strings.Contains(out, "scanner error") {
		t.Errorf("a scanner error was logged during the shutdown:\n%s", out)
	}
}
