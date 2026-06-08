package main

import (
	"bufio"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunStopSignalsShutdown(t *testing.T) {
	s, sock := newRunningServer(t)
	t.Setenv("CLAUDE_RPC_TOKEN", testToken)

	if err := runStop(sock); err != nil {
		t.Fatalf("runStop: %v", err)
	}
	select {
	case <-s.shutdown:
	case <-time.After(2 * time.Second):
		t.Error("runStop did not trigger server.shutdown")
	}
}

// runStop is best-effort (matching the reference): an empty or unreachable
// socket is a silent no-op (returns nil), not an error.
func TestRunStopBestEffort(t *testing.T) {
	if err := runStop(""); err != nil {
		t.Errorf("empty socket should be a silent no-op, got %v", err)
	}
	if err := runStop(filepath.Join(t.TempDir(), "nope.sock")); err != nil {
		t.Errorf("unreachable socket should be a silent no-op, got %v", err)
	}
}

// runBridge, unlike -stop, treats a dial failure as a hard error and wraps it
// "dial server: <err>" (the reference's framing).
func TestRunBridgeDialError(t *testing.T) {
	err := runBridge(filepath.Join(t.TempDir(), "nope.sock"))
	if err == nil || !strings.Contains(err.Error(), "dial server:") {
		t.Errorf("runBridge on a missing socket = %v, want a 'dial server:' error", err)
	}
}

// runStop echoes the server's response frame to stdout. The real daemon is silent
// on shutdown, so we point runStop at a raw socket server that replies with a
// known frame, swap os.Stdout for a pipe, and assert the reply is written. This
// pins the `if n > 0` write guard: a negated guard would drop the response.
func TestRunStopEchoesResponse(t *testing.T) {
	// Short socket dir (macOS sun_path is ~104 bytes), mirroring the harness.
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	const reply = `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n"
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = bufio.NewReader(c).ReadString('\n') // consume the shutdown request
		_, _ = io.WriteString(c, reply)
	}()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Setenv("CLAUDE_RPC_TOKEN", "tok")

	stopErr := runStop(sock)
	_ = w.Close()
	os.Stdout = old

	if stopErr != nil {
		t.Fatalf("runStop: %v", stopErr)
	}
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), `"result"`) {
		t.Errorf("runStop did not echo the server reply to stdout; got %q (the n>0 write was skipped)", out)
	}
}

// TestRunBridgeRelays drives a request through the stdio<->socket relay: swap
// os.Stdin/os.Stdout for pipes, write a ping, and read the daemon's reply back
// out of stdout. Closing stdin ends the relay.
func TestRunBridgeRelays(t *testing.T) {
	_, sock := newRunningServer(t)

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() { os.Stdin, os.Stdout = oldIn, oldOut })

	done := make(chan error, 1)
	go func() { done <- runBridge(sock) }()

	if _, err := io.WriteString(inW, `{"jsonrpc":"2.0","id":7,"method":"server.ping","auth":"`+testToken+`"}`+"\n"); err != nil {
		t.Fatal(err)
	}

	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(outR)
		if sc.Scan() {
			lines <- sc.Text()
		} else {
			lines <- ""
		}
	}()

	select {
	case line := <-lines:
		if !strings.Contains(line, `"pong":true`) || !strings.Contains(line, `"id":7`) {
			t.Errorf("relayed reply = %q, want a ping response", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the bridged reply")
	}

	_ = inW.Close() // EOF on stdin → the stdin->socket copy finishes → runBridge returns
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("runBridge did not return after stdin closed")
	}
	_ = outW.Close()
}
