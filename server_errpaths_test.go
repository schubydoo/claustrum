package main

import (
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/metrics"
)

// conn.Conn.WriteJSON surfaces a marshal failure instead of writing a broken frame; the
// error must come back before any bytes touch the connection (nc stays nil —
// reaching a write would panic).
func TestWriteJSONMarshalError(t *testing.T) {
	c := conn.New(nil, nil, 0, nil, nil, nil)
	if err := c.WriteJSON(map[string]any{"bad": make(chan int)}); err == nil {
		t.Error("writeJSON of an unmarshalable value succeeded, want error")
	}
}

// serveConn skips blank request lines (a client sending "\n" keepalives or a
// trailing newline after a frame) without dispatching or replying, and the
// connection keeps serving afterwards.
func TestServeConnSkipsEmptyLines(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.send("") // a bare newline: no reply, no closed connection
	cl.send("")
	reply := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`)))
	if !strings.Contains(reply, `"pong":true`) {
		t.Fatalf("ping after empty lines = %s, want pong:true", reply)
	}
}

// metrics.StartServer propagates an unbindable address as an error (runServe
// logs it and keeps going — the socket, not the metrics endpoint, is the job).
func TestStartMetricsServerBadAddr(t *testing.T) {
	if ln, err := metrics.StartServer("127.0.0.1:99999999"); err == nil {
		_ = ln.Close()
		t.Error("metrics.StartServer on an invalid port succeeded, want error")
	}
}
