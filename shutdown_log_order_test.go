package main

import (
	"log"
	"net"
	"strings"
	"testing"
	"time"
)

// closeMarkConn logs a marker line into the captured log just before each Close
// reaches the wrapped conn. The marker puts the socket close in the same ordered
// stream as the daemon's own log lines.
type closeMarkConn struct{ net.Conn }

func (m closeMarkConn) Close() error {
	log.Printf("MARK close")
	return m.Conn.Close()
}

// On server.shutdown the received and shutdown-requested lines come before the
// teardown closes the connection. The connection's closed line comes after that
// close. The reference showed this order in its traced runs (measured).
func TestShutdownLogsBeforeConnectionClose(t *testing.T) {
	s := newTestServer(t)
	cli, srv := net.Pipe()
	t.Cleanup(func() { _ = cli.Close() })
	c := &conn{nc: closeMarkConn{srv}}
	s.conns[c] = struct{}{}
	dropConnsOnShutdown(t, s)

	out := captureLog(t, func() {
		served := make(chan struct{})
		go func() { defer close(served); s.serveConn(c) }()
		replied := make(chan struct{})
		go func() {
			defer close(replied)
			s.dispatch(nil, []byte(`{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}`))
		}()
		for _, ch := range []chan struct{}{replied, served} {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				t.Fatal("server.shutdown did not finish the teardown of the connection")
			}
		}
	})

	at := func(sub string) int {
		i := strings.Index(out, sub)
		if i < 0 {
			t.Fatalf("log has no %q; got:\n%s", sub, out)
		}
		return i
	}
	recv := at("INFO  [ServerHandler] server.shutdown received over RPC\n")
	req := at("INFO  [Server] shutdown requested\n")
	closed := at("MARK close")
	line := at("INFO  [Server] Connection closed: pipe\n")
	if recv >= req || req >= closed {
		t.Errorf("want received, then shutdown requested, then the connection close; got:\n%s", out)
	}
	if closed >= line {
		t.Errorf("want the connection close before its Connection closed line; got:\n%s", out)
	}
}
