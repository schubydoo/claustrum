package main

import (
	"net"
	"os"
	"path/filepath"
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

// serveConnOnPipe runs one connection's read loop over a net.Pipe against a
// minimal server, returning the log output once serveConn has returned. Driving
// serveConn directly (rather than through the socket harness) keeps the capture
// buffer to a single connection's lines and makes the ordering assertion below
// deterministic: fn only returns after the deferred "Connection closed" ran.
// A net.Pipe address renders as "pipe" on every platform, so the assertions can
// scope themselves to this connection.
func serveConnOnPipe(t *testing.T, write func(net.Conn)) string {
	t.Helper()
	s := &server{
		token:    testToken,
		procs:    newTestProcManager(t),
		conns:    make(map[*conn]struct{}),
		shutdown: make(chan struct{}),
	}
	cli, srv := net.Pipe()
	return captureLog(t, func() {
		done := make(chan struct{})
		go func() { defer close(done); s.serveConn(&conn{nc: srv}) }()
		write(cli)
		_ = cli.Close()
		<-done
	})
}

// An over-cap request line ends the read loop with a scanner error rather than a
// clean EOF, and that arm is otherwise silent — no reply goes out. The daemon
// reports it the way the reference does, in the reference's measured order: the
// scanner error precedes "Connection closed", which comes from claustrum's
// cleanup defer.
func TestOversizeRequestLogsScannerError(t *testing.T) {
	out := serveConnOnPipe(t, func(cli net.Conn) {
		// Deliberately unterminated: bufio gives up at the cap either way, and the
		// client-side write unblocks when serveConn's defer closes its end.
		_, _ = cli.Write([]byte(strings.Repeat("x", 1024*1024)))
	})

	i := strings.Index(out, "[Server] scanner error on pipe")
	if i < 0 {
		t.Fatalf("no scanner error logged; got:\n%s", out)
	}
	if !strings.Contains(out[i:], "bufio.Scanner: token too long") {
		t.Errorf("scanner error carries the wrong cause; got:\n%s", out)
	}
	j := strings.Index(out, "[Server] Connection closed: pipe")
	if j < 0 {
		t.Fatalf("no close line logged; got:\n%s", out)
	}
	if j < i {
		t.Errorf("scanner error must precede Connection closed; got:\n%s", out)
	}
}

// The counterpart: sc.Err() is nil on a clean EOF, so an ordinary disconnect
// stays as quiet as it was before the check existed.
func TestCleanDisconnectLogsNoScannerError(t *testing.T) {
	out := serveConnOnPipe(t, func(net.Conn) {})
	if strings.Contains(out, "scanner error") {
		t.Errorf("clean disconnect logged a scanner error; got:\n%s", out)
	}
}

// The other quiet arm, and the reason for the net.ErrClosed exclusion: when the
// DAEMON closes the connection — which closeAll does to every client on the
// graceful-shutdown path — the pending read fails with net.ErrClosed. Measured
// on a server.shutdown with two connections open, the reference's log carries no
// scanner-error line; an unfiltered sc.Err() check made claustrum emit one per
// connected client. It needs a real socket, because a net.Pipe close reports
// io.ErrClosedPipe instead.
func TestServerInitiatedCloseLogsNoScannerError(t *testing.T) {
	// Short socket path on purpose — see newRunningServer.
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type accepted struct {
		nc  net.Conn
		err error
	}
	ch := make(chan accepted, 1)
	go func() { nc, err := ln.Accept(); ch <- accepted{nc, err} }()
	cli, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()
	a := <-ch
	if a.err != nil {
		t.Fatalf("accept: %v", a.err)
	}

	s := &server{
		token:    testToken,
		procs:    newTestProcManager(t),
		conns:    make(map[*conn]struct{}),
		shutdown: make(chan struct{}),
	}
	out := captureLog(t, func() {
		done := make(chan struct{})
		go func() { defer close(done); s.serveConn(&conn{nc: a.nc}) }()
		_ = a.nc.Close() // what closeAll does on server.shutdown
		<-done
	})
	if strings.Contains(out, "scanner error") {
		t.Errorf("daemon-initiated close logged a scanner error; got:\n%s", out)
	}
}
