package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// TestSocketReattachSupersedesPreviousConnection drives the supersede through a
// real socket, so it covers the wiring and not just procManager.reattach.
//
// The unit tests in reattach_supersede_test.go build their conns by hand, which
// means they cannot notice the accept loop failing to give a connection its
// deliberate-close flag. That failure mode is not hypothetical: the test helper
// itself had it, and every supersede assertion passed while nothing was
// superseded.
//
// Measured against both reference binaries on an ephemeral linux VM
// (scratch/probe/ref90-capture.md section B): after a second connection
// reattaches, 19f30c46 still answers server.ping on the first connection, and
// 90fca6e6 fails the very next write to it.
func TestSocketReattachSupersedesPreviousConnection(t *testing.T) {
	sock := startSocketServer(t)

	// Connection 1 is raw rather than a testClient, because the assertion is on
	// the socket itself: the daemon must close it. A testClient's read loop owns
	// the conn and would swallow that.
	nc1, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer nc1.Close()
	rd1 := bufio.NewReader(nc1)

	send := func(w io.Writer, id int, method string, params map[string]any) error {
		t.Helper()
		b, mErr := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": method,
			"auth": testToken, "params": params,
		})
		if mErr != nil {
			t.Fatalf("marshal %s: %v", method, mErr)
		}
		_, wErr := w.Write(append(b, '\n'))
		return wErr
	}

	exe, env := helperCommand(t, "sleep")
	if err := send(nc1, 1, "process.spawn", map[string]any{
		"id": "SUP", "command": exe, "args": []string{"30"}, "env": env,
	}); err != nil {
		t.Fatalf("spawn write: %v", err)
	}
	if _, err := rd1.ReadString('\n'); err != nil {
		t.Fatalf("spawn reply: %v", err)
	}

	// The control that must keep passing: before any reattach, connection 1 is
	// usable. Without it a daemon that closed every connection at spawn would pass
	// the assertion below.
	if err := send(nc1, 2, "server.ping", nil); err != nil {
		t.Fatalf("ping write before the reattach: %v", err)
	}
	if line, err := rd1.ReadString('\n'); err != nil || !strings.Contains(line, `"pong":true`) {
		t.Fatalf("ping before the reattach = %q, err=%v; want a pong", line, err)
	}

	cl2 := dial(t, sock)
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "process.reattach",
		"auth": testToken, "params": map[string]any{"id": "SUP"},
	})
	if err != nil {
		t.Fatalf("marshal reattach: %v", err)
	}
	if reply := string(cl2.call(string(b))); !strings.Contains(reply, `"found":true`) {
		t.Fatalf("reattach = %s, want found true", reply)
	}

	// Connection 1 must now be closed by the daemon. A read is the honest check:
	// a write can sit in the socket buffer and succeed against a closed peer.
	if err := nc1.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	line, err := rd1.ReadString('\n')
	if err == nil {
		t.Fatalf("connection 1 is still open and delivered %q; the reference closes it", line)
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("connection 1 is still open after 5s; the reference closes it on reattach")
	}

	// The reattaching connection keeps working.
	if reply := string(cl2.call(string(b))); !strings.Contains(reply, `"found":true`) {
		t.Errorf("the reattaching connection stopped working: %s", reply)
	}
}
