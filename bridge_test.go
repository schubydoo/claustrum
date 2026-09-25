package main

import (
	"bufio"
	"bytes"
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

	var out bytes.Buffer
	runStop(sock, &out)
	select {
	case <-s.shutdown:
	case <-time.After(2 * time.Second):
		t.Error("runStop did not trigger server.shutdown")
	}
	if got := out.String(); got != "stopped\n" {
		t.Errorf("runStop against a live daemon printed %q, want %q", got, "stopped\n")
	}
}

// A failed connect with no daemon.lock prints "none" (measured against f6010b97
// and 90fca6e6 on a Linux VM). It also removes a stale daemon.token beside the
// socket path. The references delete it in this case, and a
// pre-fix claustrum left it (measured, 10/10 each).
func TestRunStopNoneRemovesStaleToken(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "nope.sock")
	token := filepath.Join(dir, persistedTokenName)
	if err := os.WriteFile(token, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	runStop(sock, &out)
	if got := out.String(); got != "none\n" {
		t.Errorf("runStop on a missing socket printed %q, want %q", got, "none\n")
	}
	if _, err := os.Lstat(token); !os.IsNotExist(err) {
		t.Errorf("stale daemon.token still present after a failed connect (Lstat err = %v)", err)
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

// runStop prints the word "stopped" and never the reply frame. -stop is a
// control command, not a relay, so a {"ok":true} shutdown reply must never reach
// its stdout. A real daemon's reply races its teardown, so this test uses a fake
// listener that always replies. Every connect-success shape prints the same
// word: a reply, garbage, a close with no reply, and the read deadline. That is
// measured against f6010b97 and 90fca6e6 on a Linux VM. After a successful
// connect, -stop leaves the socket path in place.
func TestRunStopConnectSuccessWords(t *testing.T) {
	old := stopReplyTimeout
	stopReplyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { stopReplyTimeout = old })

	const request = `{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}` + "\n"
	cases := []struct {
		name  string
		serve func(c net.Conn) // runs after the request is read, then the conn closes
	}{
		{"reply", func(c net.Conn) {
			_, _ = io.WriteString(c, `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`+"\n")
		}},
		{"garbage", func(c net.Conn) {
			_, _ = io.WriteString(c, "\x00garbage\xff not json\n")
		}},
		{"close without reply", func(net.Conn) {}},
		{"never replies", func(net.Conn) { time.Sleep(2 * stopReplyTimeout) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			got := make(chan string, 1)
			go func() {
				c, err := ln.Accept()
				if err != nil {
					got <- ""
					return
				}
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				tc.serve(c)
				got <- line
			}()

			var out bytes.Buffer
			runStop(sock, &out)
			if got := out.String(); got != "stopped\n" {
				t.Errorf("runStop printed %q, want %q", got, "stopped\n")
			}
			if line := <-got; line != request {
				t.Errorf("request frame = %q, want %q", line, request)
			}
			if _, err := os.Lstat(sock); err != nil {
				t.Errorf("socket path gone after a successful connect (Lstat err = %v); -stop must not unlink it", err)
			}
		})
	}
}

// TestRunBridgeRelays drives a request through the stdio<->socket relay: swap
// os.Stdin/os.Stdout for pipes, write a ping, and read the daemon's reply back
// out of stdout. Closing stdin half-closes the socket. The daemon then closes
// the connection, and that ends the relay.
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

	_ = inW.Close() // stdin EOF → half-close → the daemon closes → runBridge returns
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("runBridge did not return after the daemon closed")
	}
	_ = outW.Close()
}
