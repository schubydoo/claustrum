//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

// TestHonorListenPipeWindows: on Windows the transport is real, so the flag passes
// through unchanged.
func TestHonorListenPipeWindows(t *testing.T) {
	if !honorListenPipe(true) {
		t.Error("honorListenPipe(true) = false, want true on Windows")
	}
	if honorListenPipe(false) {
		t.Error("honorListenPipe(false) = true, want false")
	}
}

// TestPipeTransportServesJSONRPC boots the production named-pipe transport
// (startPipeTransport + the real acceptLoop/serveConn dispatch), discovers the
// pipe name from rpc.pipe exactly as a client would, and drives the same JSON-RPC
// battery over it: an authenticated request round-trips identically to the socket,
// and an unauthenticated one is denied — the "no valid token → denied" half of the
// security guardrail, over real pipe I/O. (The owner-only-DACL half is asserted in
// TestOwnerOnlySDDL; impersonating a foreign SID in CI isn't feasible.)
func TestPipeTransportServesJSONRPC(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "rpc.sock")

	pln, err := startPipeTransport(sock)
	if err != nil {
		t.Fatalf("startPipeTransport: %v", err)
	}
	s := &server{
		token:    testToken,
		pipeLn:   pln,
		procs:    newProcManager(),
		conns:    make(map[*conn]struct{}),
		shutdown: make(chan struct{}),
	}
	go s.acceptLoop(s.pipeLn)
	t.Cleanup(func() {
		_ = pln.Close()
		removePipeNameFile(sock)
		s.procs.killAll()
	})

	// A client learns the opaque pipe name from rpc.pipe, written beside the socket
	// before the pipe began accepting.
	nameBytes, err := os.ReadFile(filepath.Join(dir, pipeNameFileName))
	if err != nil {
		t.Fatalf("read %s: %v", pipeNameFileName, err)
	}
	name := string(nameBytes)
	if name != pln.Addr().String() {
		t.Fatalf("rpc.pipe = %q, but listener addr = %q", name, pln.Addr())
	}

	timeout := 5 * time.Second
	nc, err := winio.DialPipe(name, &timeout)
	if err != nil {
		t.Fatalf("dial pipe %s: %v", name, err)
	}
	cl := &testClient{t: t, nc: nc}
	go cl.readLoop()
	t.Cleanup(func() { _ = nc.Close() })

	// Authenticated request round-trips through the exact same dispatch as AF_UNIX.
	reply := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`)))
	if !strings.Contains(reply, `"pong":true`) {
		t.Fatalf("authed ping over pipe = %s, want pong:true", reply)
	}

	// A wrong token is rejected with the same Unauthorized error as on the socket.
	denied := string(cl.call(`{"jsonrpc":"2.0","id":2,"method":"server.ping","auth":"wrong-token"}`))
	if !strings.Contains(denied, "-32001") || !strings.Contains(denied, "Unauthorized") {
		t.Fatalf("bad-token ping over pipe = %s, want -32001 Unauthorized", denied)
	}
}
