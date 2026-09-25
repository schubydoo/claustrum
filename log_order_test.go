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
	p := indexOrFail(t, out, "WARN  [Server] Parse error: invalid character 'o' in literal null (expecting 'u')\n")
	w := indexOrFail(t, out, "MARK write")
	if p > w {
		t.Errorf("parse-error line must precede the reply write; got:\n%s", out)
	}
}

// The auth-error line is logged before the -32001 reply is written.
func TestUnauthorizedLoggedBeforeReplyWrite(t *testing.T) {
	out := serveMarkedConn(t, `{"jsonrpc":"2.0","id":7,"method":"server.ping"}`+"\n", true)
	u := indexOrFail(t, out, "WARN  [Server] Unauthorized request: method=server.ping, id=7\n")
	w := indexOrFail(t, out, "MARK write")
	if u > w {
		t.Errorf("auth-error line must precede the reply write; got:\n%s", out)
	}
}

// At a client EOF the "Connection closed" line is logged before the socket close.
func TestConnectionClosedLoggedBeforeClose(t *testing.T) {
	out := serveMarkedConn(t, "", false)
	c := indexOrFail(t, out, "INFO  [Server] Connection closed: pipe\n")
	k := indexOrFail(t, out, "MARK close")
	if c > k {
		t.Errorf("Connection closed must precede the socket close; got:\n%s", out)
	}
}
