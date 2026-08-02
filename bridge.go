package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// runBridge is a dumb stdio<->unix-socket relay. It does NOT inject auth; the
// stream it relays must already carry "auth" per request. This is what an SSH
// session attaches to. A dial failure is a hard error (wrapped "dial server:",
// matching the reference) — unlike best-effort -stop.
func runBridge(socket string) error {
	nc, err := net.Dial("unix", socket)
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}
	defer nc.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(nc, os.Stdin); done <- struct{}{} }()
	go func() { _, _ = io.Copy(os.Stdout, nc); done <- struct{}{} }()
	<-done
	return nil
}

// runStop sends an unauthenticated server.shutdown RPC to a running daemon.
// ⚠️ this stops the daemon and drops its sessions. It is best-effort (matching
// the reference): a missing or unreachable daemon is a silent no-op, not an
// error.
// stopReplyTimeout bounds how long -stop waits for the daemon's reply to its
// shutdown request. 2s, measured against the reference. var so tests can shrink it.
var stopReplyTimeout = 2 * time.Second

func runStop(socket string) error {
	nc, err := net.Dial("unix", socket)
	if err != nil {
		return nil
	}
	defer nc.Close()
	// No auth member. The reference's -stop sends exactly this frame, and its
	// daemon does not authenticate server.shutdown. Captured off a fake listener
	// with CLAUDE_RPC_TOKEN deliberately SET, so the omission is known to be real
	// and not just an unset variable:
	//
	//	reference : {"jsonrpc":"2.0","id":1,"method":"server.shutdown"}
	//	claustrum : {"jsonrpc":"2.0","id":1,"method":"server.shutdown","auth":"…"}
	//
	// CLAUDE_RPC_TOKEN is no longer read here at all — -stop needs no credential,
	// which is what lets `server --stop --socket <sock>` work from a bare SSH
	// command line the way the Desktop client invokes it.
	_, _ = fmt.Fprint(nc, `{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}`+"\n")
	// Bound the wait for the shutdown reply. A daemon that accepts the connection
	// and then never answers — wedged, or a stale socket now owned by something
	// else — would otherwise hang -stop forever, since a bare Read has no
	// deadline. Measured at 5db5e4a: against a socket that accepts and never
	// replies the reference returns in 2.030s (three runs, and its Stop carries a
	// 2e9 ns immediate), where claustrum was still blocked when killed at 45s.
	//
	// A deadline error is deliberately ignored, exactly like the read error
	// already was: -stop is best-effort and always reports success. The shutdown
	// request has already been written by this point, so a daemon that is merely
	// slow to answer still stops.
	_ = nc.SetReadDeadline(time.Now().Add(stopReplyTimeout))
	// Read the reply and DISCARD it. The reference prints nothing, so neither do
	// we. Against claustrum's own daemon there is now no reply to read at all —
	// server.shutdown answers nothing and closes — so this read normally returns
	// EOF at once. It still matters against a foreign or wedged listener, and the
	// original measurement stands: the reference's stdout stayed empty where
	// claustrum echoed the raw frame.
	//
	// -stop is a control command, not a relay; a caller parsing its output should
	// not have a JSON-RPC frame appear on any path.
	buf := make([]byte, 4096)
	_, _ = nc.Read(buf)
	return nil
}
