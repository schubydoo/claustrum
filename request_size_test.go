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
