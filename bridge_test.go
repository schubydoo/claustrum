package main

import (
	"bufio"
	"io"
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

func TestRunStopErrors(t *testing.T) {
	if runStop("") == nil {
		t.Error("an empty socket should error")
	}
	if runStop(filepath.Join(t.TempDir(), "nope.sock")) == nil {
		t.Error("an unreachable socket should error")
	}
}

func TestRunBridgeRequiresSocket(t *testing.T) {
	if runBridge("") == nil {
		t.Error("an empty socket should error")
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
