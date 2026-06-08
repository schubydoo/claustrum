package main

import (
	"fmt"
	"io"
	"net"
	"os"
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

// runStop sends a server.shutdown RPC (authenticated with CLAUDE_RPC_TOKEN) to a
// running daemon. ⚠️ this stops the daemon and drops its sessions. It is
// best-effort (matching the reference): a missing or unreachable daemon is a
// silent no-op, not an error.
func runStop(socket string) error {
	nc, err := net.Dial("unix", socket)
	if err != nil {
		return nil
	}
	defer nc.Close()
	tok := os.Getenv("CLAUDE_RPC_TOKEN")
	_, _ = fmt.Fprintf(nc, `{"jsonrpc":"2.0","id":1,"method":"server.shutdown","auth":%q}`+"\n", tok)
	buf := make([]byte, 4096)
	n, _ := nc.Read(buf)
	if n > 0 {
		_, _ = os.Stdout.Write(buf[:n])
	}
	return nil
}
