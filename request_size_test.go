package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

// pingLineOfLen builds a valid server.ping request whose line length (excluding
// the framing newline) is exactly n bytes, padded via an ignored params field.
func pingLineOfLen(n int) string {
	prefix := `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"` + testToken + `","params":{"pad":"`
	suffix := `"}}`
	return prefix + strings.Repeat("x", n-len(prefix)-len(suffix)) + suffix
}

// sendRaw dials the socket, writes one framed line, and returns the first reply
// line (or "" if the daemon closed the connection without replying). The write
// runs in a goroutine so an over-cap line — where the daemon stops reading and
// closes mid-write — doesn't block the read.
func sendRaw(t *testing.T, sock, line string) string {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer nc.Close()
	go func() { _, _ = nc.Write([]byte(line + "\n")) }()
	_ = nc.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	n, _ := nc.Read(buf)
	return string(buf[:n])
}

// The daemon caps a single request line at 1 MiB (bufio maxTokenSize =
// 1024*1024), matching the reference to the exact byte: a 1048575-byte line is
// served, a 1048576-byte line closes the connection with no reply.
func TestRequestSizeCap(t *testing.T) {
	sock := startSocketServer(t)

	if reply := sendRaw(t, sock, pingLineOfLen(1024*1024-1)); !strings.Contains(reply, `"pong":true`) {
		t.Errorf("1048575-byte request: reply=%q, want a pong", reply)
	}
	if reply := sendRaw(t, sock, pingLineOfLen(1024*1024)); reply != "" {
		t.Errorf("1048576-byte request: reply=%q, want none (connection dropped)", reply)
	}
}

// A panicking handler must not take the daemon down — the per-request goroutine
// recovers, replies -32603 "internal panic: <v>", and the connection stays
// usable for the next request. Matches the reference's per-request panic
// isolation; the path is otherwise unreachable, so it is provoked here through
// the dispatchRequest seam. Reverting the recover makes this CRASH the test
// binary (an unrecovered goroutine panic kills the process), which is the
// mutant signal — run it isolated when checking.
func TestHandlerPanicIsRecovered(t *testing.T) {
	old := dispatchRequest
	dispatchRequest = func(s *server, c *conn, raw []byte) *response {
		panic("boom-" + string(raw[:0]))
	}
	t.Cleanup(func() { dispatchRequest = old })

	sock := startSocketServer(t)

	// First request hits the panicking dispatch: expect the -32603 frame, not a
	// dropped connection or a dead daemon.
	reply := sendRaw(t, sock, `{"jsonrpc":"2.0","id":7,"method":"server.ping","auth":"`+testToken+`"}`)
	for _, want := range []string{`"id":7`, `"code":-32603`, "internal panic:"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("panic reply = %q, missing %q", reply, want)
		}
	}

	// The daemon is still alive: restore normal dispatch and a fresh request works.
	dispatchRequest = old
	if reply := sendRaw(t, sock, `{"jsonrpc":"2.0","id":8,"method":"server.ping","auth":"`+testToken+`"}`); !strings.Contains(reply, `"pong":true`) {
		t.Errorf("daemon dead after a handler panic: reply=%q, want a pong", reply)
	}
}
