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

	"github.com/schubydoo/claustrum/internal/tokenfile"
)

// adoptSocketDir makes a short folder with a daemon.token of token and returns the
// socket path in it.
func adoptSocketDir(t *testing.T, token string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if token != "" {
		if err := os.WriteFile(filepath.Join(dir, tokenfile.Name), []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "s.sock")
}

// TestBridgeAdoptFrameBytes: the first frame of the bridge, byte for byte, in the
// shape of the raw rows of c9125eb8 (measured on Linux, macOS and Windows): the
// members in name order, the env in its own order, noLoginShell last and only for
// the value no-login-shell. Other values send no frame (measured), and so do a big
// env (measured) and a missing token file (claustrum's own).
func TestBridgeAdoptFrameBytes(t *testing.T) {
	sock := adoptSocketDir(t, "test-token-0123")
	env := []string{"PATH=/usr/bin:/bin", "HOME=/h", "USER=u", "CLAUDE_SSH_ADOPT_ENV=1", "FOO=bar", "CLAUDE_RPC_TOKEN=secret", "EXTRA=1"}
	const head = `{"auth":"test-token-0123","id":"bridge","jsonrpc":"2.0","method":"server.adoptEnv","params":{"env":["PATH=/usr/bin:/bin","HOME=/h","USER=u","CLAUDE_SSH_ADOPT_ENV=1","FOO=bar","EXTRA=1"]`
	for _, tc := range []struct{ value, want string }{
		{"1", head + "}}\n"},
		{"no-login-shell", head + `,"noLoginShell":true}}` + "\n"},
		{"yes", ""},
		{"true", ""},
		{"0", ""},
		{"", ""},
	} {
		t.Setenv(bridgeAdoptEnv, tc.value)
		if got := string(bridgeAdoptFrame(sock, env)); got != tc.want {
			t.Errorf("%s=%q: frame %q, want %q", bridgeAdoptEnv, tc.value, got, tc.want)
		}
	}
	t.Setenv(bridgeAdoptEnv, "1")
	bigEnv := []string{"A=" + strings.Repeat("v", 100000), "B=" + strings.Repeat("v", 100000), "C=" + strings.Repeat("v", 100000)}
	if got := bridgeAdoptFrame(sock, bigEnv); got != nil {
		t.Errorf("an env of 300000 bytes gave a frame of %d bytes, want none", len(got))
	}
	if got := bridgeAdoptFrame(adoptSocketDir(t, ""), env); got != nil {
		t.Errorf("a missing daemon.token gave the frame %q, want none", got)
	}
}

// bridgePipes swaps os.Stdin and os.Stdout for pipes until the test ends. It
// returns the write end of stdin and the lines of stdout.
func bridgePipes(t *testing.T) (*os.File, <-chan string) {
	t.Helper()
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
	t.Cleanup(func() {
		os.Stdin, os.Stdout = oldIn, oldOut
		_ = inW.Close()
		_ = inR.Close()
		_ = outW.Close()
	})
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return inW, lines
}

// readLineWithin reads one line of br, or fails the test after 5 s.
func readLineWithin(t *testing.T, nc net.Conn, br *bufio.Reader) string {
	t.Helper()
	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read from the bridge: %v (got %q)", err, line)
	}
	return line
}

// TestRunBridgeSendsAdoptFrameFirst: with CLAUDE_SSH_ADOPT_ENV=1 the first line at
// the socket is the frame. With no reply the bridge dials again and sends it again
// (measured on Windows). An error reply does not stop the relay, and that reply does
// not reach stdout (measured on Linux). The relayed ping follows.
func TestRunBridgeSendsAdoptFrameFirst(t *testing.T) {
	old := bridgeAdoptWait
	bridgeAdoptWait = 500 * time.Millisecond
	t.Cleanup(func() { bridgeAdoptWait = old })
	t.Setenv(bridgeAdoptEnv, "1")
	sock := adoptSocketDir(t, "tok")
	want := string(bridgeAdoptFrame(sock, os.Environ()))
	if want == "" {
		t.Fatal("this environment gives no frame")
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// A bridge that never dials again fails the test here, not at the time limit.
	if err := ln.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	stdin, stdout := bridgePipes(t)
	done, ended := make(chan error, 1), make(chan struct{})
	go func() {
		done <- runBridge(sock)
		close(ended)
	}()
	var conns []net.Conn
	// This cleanup runs before the one of bridgePipes, so the bridge has returned
	// before the globals go back.
	t.Cleanup(func() {
		_ = ln.Close()
		for _, c := range conns {
			_ = c.Close()
		}
		select {
		case <-ended:
		case <-time.After(10 * time.Second):
			t.Error("runBridge did not return at the end of the test")
		}
	})

	first, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	conns = append(conns, first)
	if got := readLineWithin(t, first, bufio.NewReader(first)); got != want {
		t.Fatalf("first line %q, want the frame %q", got, want)
	}
	// No reply: the bridge closes this connection and dials again.
	second, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	conns = append(conns, second)
	br := bufio.NewReader(second)
	if got := readLineWithin(t, second, br); got != want {
		t.Fatalf("first line after the dial again %q, want the frame %q", got, want)
	}
	if _, err := io.WriteString(second, `{"jsonrpc":"2.0","id":"bridge","error":{"code":-32601,"message":"Unknown method: server.adoptEnv"}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	// Wait past the adopt wait: the relay must not keep its read deadline.
	time.Sleep(2 * bridgeAdoptWait)
	const ping = `{"jsonrpc":"2.0","id":7,"method":"server.ping"}` + "\n"
	if _, err := io.WriteString(stdin, ping); err != nil {
		t.Fatal(err)
	}
	if got := readLineWithin(t, second, br); got != ping {
		t.Fatalf("relayed line %q, want %q", got, ping)
	}
	const pong = `{"jsonrpc":"2.0","id":7,"result":{"pong":true}}`
	if _, err := io.WriteString(second, pong+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-stdout:
		if got != pong {
			t.Errorf("stdout line %q, want %q", got, pong)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no line on stdout")
	}
	_ = second.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runBridge = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("runBridge did not return after the listener closed")
	}
}
