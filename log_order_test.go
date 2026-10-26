package main

import (
	"bufio"
	"log"
	"net"
	"strings"
	"testing"
)

// markConn logs a marker line into the captured log just before each Write and
// Close reaches the wrapped conn. The markers put the socket writes and the
// socket close in the same ordered stream as the daemon's own log lines.
type markConn struct{ net.Conn }

func (m markConn) Write(p []byte) (int, error) {
	log.Printf("MARK write")
	return m.Conn.Write(p)
}

func (m markConn) Close() error {
	log.Printf("MARK close")
	return m.Conn.Close()
}

// serveMarkedConn runs one connection's read loop over a marked net.Pipe. It
// sends line, reads one reply line when wantReply is set, then closes the
// client end. It returns the captured log once serveConn has returned.
func serveMarkedConn(t *testing.T, line string, wantReply bool) string {
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
		go func() { defer close(done); s.serveConn(&conn{nc: markConn{srv}}) }()
		if line != "" {
			if _, err := cli.Write([]byte(line)); err != nil {
				t.Errorf("client write: %v", err)
			}
		}
		if wantReply {
			if _, err := bufio.NewReader(cli).ReadString('\n'); err != nil {
				t.Errorf("client read: %v", err)
			}
		}
		_ = cli.Close()
		<-done
	})
}

// showTokenOn sends one authorized ping on cli and reads its reply. The daemon then
// holds the token mark of that connection.
func showTokenOn(t *testing.T, cli net.Conn) {
	t.Helper()
	line := `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"` + testToken + `"}` + "\n"
	if _, err := cli.Write([]byte(line)); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if _, err := bufio.NewReader(cli).ReadString('\n'); err != nil {
		t.Fatalf("client read: %v", err)
	}
}

// indexOrFail returns the index of sub in out, and fails the test if it is absent.
func indexOrFail(t *testing.T, out, sub string) int {
	t.Helper()
	i := strings.Index(out, sub)
	if i < 0 {
		t.Fatalf("log has no %q; got:\n%s", sub, out)
	}
	return i
}

// The parse-error line is logged before the -32700 reply is written. The
// reference logs "Parse error: <json error>" for this input, and a later
// write failure for the same reply follows that line (measured).
func TestParseErrorLoggedBeforeReplyWrite(t *testing.T) {
	out := serveMarkedConn(t, "not json\n", true)
	p := indexOrFail(t, out, "WARN  [Server] Parse error number 1: invalid character 'o' in literal null (expecting 'u')\n")
	w := indexOrFail(t, out, "MARK write")
	if p > w {
		t.Errorf("parse-error line must precede the reply write; got:\n%s", out)
	}
}

// The auth-error line is logged before the -32001 reply is written.
func TestUnauthorizedLoggedBeforeReplyWrite(t *testing.T) {
	out := serveMarkedConn(t, `{"jsonrpc":"2.0","id":7,"method":"server.ping"}`+"\n", true)
	u := indexOrFail(t, out, "WARN  [Server] Unauthorized request number 1: method=\"server.ping\"\n")
	w := indexOrFail(t, out, "MARK write")
	if u > w {
		t.Errorf("auth-error line must precede the reply write; got:\n%s", out)
	}
}

// At a client EOF the "Connection closed" line is logged before the socket close.
func TestConnectionClosedLoggedBeforeClose(t *testing.T) {
	out := serveMarkedConn(t, `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"`+testToken+`"}`+"\n", true)
	c := indexOrFail(t, out, "INFO  [Server] Connection closed: \"pipe\"\n")
	k := indexOrFail(t, out, "MARK close")
	if c > k {
		t.Errorf("Connection closed must precede the socket close; got:\n%s", out)
	}
}

// A connection that never showed the token logs no New connection and no Connection
// closed line. Its close counts on the sampled close line, before the socket
// close, with the quoted label. A failed parse does not set the mark.
func TestTokenlessConnectionLogsOnlyTheCountedClose(t *testing.T) {
	out := serveMarkedConn(t, "not json\n", true)
	c := indexOrFail(t, out, "INFO  [Server] Connection number 1 that never showed the token: \"pipe\"\n")
	if k := indexOrFail(t, out, "MARK close"); c > k {
		t.Errorf("the counted close must precede the socket close; got:\n%s", out)
	}
	for _, bad := range []string{"New connection from", "Connection closed"} {
		if strings.Contains(out, bad) {
			t.Errorf("a connection with no token mark logged %q; got:\n%s", bad, out)
		}
	}
}

// The first request that passes the token check logs the New connection line, once,
// before the daemon handles the request. server.shutdown sets the mark without
// a token. A refused request sets none.
func TestNewConnectionLineComesAtTheFirstAuthorizedRequest(t *testing.T) {
	ping := `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"` + testToken + `"}` + "\n"
	out := serveMarkedConn(t, ping, true)
	if n := strings.Count(out, "INFO  [Server] New connection from: \"pipe\"\n"); n != 1 {
		t.Errorf("New connection lines = %d, want 1; got:\n%s", n, out)
	}
	if indexOrFail(t, out, "New connection from") > indexOrFail(t, out, "MARK write") {
		t.Errorf("New connection must precede the reply; got:\n%s", out)
	}
	out = serveMarkedConn(t, `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"bad"}`+"\n", true)
	if strings.Contains(out, "New connection from") {
		t.Errorf("a refused request set the token mark; got:\n%s", out)
	}
}

// The format texts of the connection log have fixed byte counts.
func TestConnectionLineFormatLengths(t *testing.T) {
	for _, c := range []struct {
		format string
		n      int
	}{
		{fmtNewConn, 32},
		{fmtConnClosed, 30},
		{fmtTokenless, 61},
		{fmtUnauth, 53},
		{fmtParseError, 34},
		{fmtIdleClose, 58},
		{fmtWriteFailed, 51},
		{fmtReplyFailed, 37},
		{fmtScannerErr, 32},
		{fmtProcExited, 50},
	} {
		if len(c.format) != c.n {
			t.Errorf("%q is %d bytes, want %d", c.format, len(c.format), c.n)
		}
	}
}
