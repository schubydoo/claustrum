package main

import (
	"bufio"
	"errors"
	"fmt"
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
// the value no-login-shell. Other values send no frame (measured), and so do a
// missing token file (measured) and a frame over 262144 bytes (measured on Linux
// and macOS).
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
	empty := adoptSocketDir(t, "")
	if err := os.WriteFile(filepath.Join(filepath.Dir(empty), tokenfile.Name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := bridgeAdoptFrame(empty, env); got != nil {
		t.Errorf("an empty daemon.token gave the frame %q, want none", got)
	}
}

// TestBridgeAdoptFrameLimits: the bridge does not test the list as the daemon does.
// A list of 9000 entries and one entry of 131072 bytes go (c9125eb8, measured). A
// frame of 262144 bytes goes, and one of 262145 does not (measured on Linux and
// macOS).
func TestBridgeAdoptFrameLimits(t *testing.T) {
	t.Setenv(bridgeAdoptEnv, "1")
	sock := adoptSocketDir(t, "tok")
	many := make([]string, 0, 9001)
	for i := range 9000 {
		many = append(many, fmt.Sprintf("V%04d=1", i))
	}
	many = append(many, "CLAUDE_SSH_ADOPT_ENV=1")
	if got := bridgeAdoptFrame(sock, many); got == nil {
		t.Error("an env of 9000 entries gave no frame, want one")
	}
	if got := bridgeAdoptFrame(sock, []string{"A=" + strings.Repeat("v", 131070)}); got == nil {
		t.Error("an entry of 131072 bytes gave no frame, want one")
	}
	// The frame of {"A=<n bytes>"} is 81 bytes plus n.
	base := len(bridgeAdoptFrame(sock, []string{"A="})) - 1
	for _, tc := range []struct {
		size int
		sent bool
	}{{262144, true}, {262145, false}} {
		env := []string{"A=" + strings.Repeat("v", 100000), "B=" + strings.Repeat("v", 100000), "C=" + strings.Repeat("v", tc.size-base-200010)}
		got := bridgeAdoptFrame(sock, env)
		switch {
		case tc.sent && len(got) != tc.size+1:
			t.Errorf("a frame of %d bytes gave %d bytes, want it with its newline", tc.size, len(got))
		case !tc.sent && got != nil:
			t.Errorf("a frame of %d bytes was sent, want none", tc.size)
		}
	}
}

// TestBridgeAdoptFrameKept: an entry that is not valid UTF-8 leaves env, and its
// name goes into kept after env (c9125eb8, measured on Linux and macOS). With
// no-login-shell, kept comes before noLoginShell (claustrum's own, not measured).
func TestBridgeAdoptFrameKept(t *testing.T) {
	sock := adoptSocketDir(t, "tok")
	env := []string{"PATH=/usr/bin:/bin", "HOME=/h", "USER=u", "CLAUDE_SSH_ADOPT_ENV=1", "K=a\xffb"}
	const head = `{"auth":"tok","id":"bridge","jsonrpc":"2.0","method":"server.adoptEnv","params":{"env":["PATH=/usr/bin:/bin","HOME=/h","USER=u","CLAUDE_SSH_ADOPT_ENV=1"],"kept":["K"]`
	for _, tc := range []struct{ value, want string }{
		{"1", head + "}}\n"},
		{"no-login-shell", head + `,"noLoginShell":true}}` + "\n"},
	} {
		t.Setenv(bridgeAdoptEnv, tc.value)
		if got := string(bridgeAdoptFrame(sock, env)); got != tc.want {
			t.Errorf("%s=%q: frame %q, want %q", bridgeAdoptEnv, tc.value, got, tc.want)
		}
	}
	// A name that is not valid UTF-8 goes into kept too (claustrum's own, not measured).
	t.Setenv(bridgeAdoptEnv, "1")
	const want = `{"auth":"tok","id":"bridge","jsonrpc":"2.0","method":"server.adoptEnv","params":{"env":["CLAUDE_SSH_ADOPT_ENV=1"],"kept":["\ufffd"]}}` + "\n"
	if got := string(bridgeAdoptFrame(sock, []string{"CLAUDE_SSH_ADOPT_ENV=1", "\xff=1"})); got != want {
		t.Errorf("a name that is not UTF-8: frame %q, want %q", got, want)
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
// (measured on Linux, macOS and Windows). An error reply does not stop the relay, and that reply does
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

// adoptBridgeRun is one runBridge against a fake listener with CLAUDE_SSH_ADOPT_ENV=1.
type adoptBridgeRun struct {
	ln     *net.UnixListener
	frame  string
	stdin  *os.File
	stdout <-chan string
	done   chan error
	conns  []net.Conn
}

// startAdoptBridge starts runBridge on a fake listener with the adopt wait wait. The
// cleanups close the listener and the accepted connections, and wait for the bridge.
func startAdoptBridge(t *testing.T, wait time.Duration) *adoptBridgeRun {
	t.Helper()
	old := bridgeAdoptWait
	bridgeAdoptWait = wait
	t.Cleanup(func() { bridgeAdoptWait = old })
	t.Setenv(bridgeAdoptEnv, "1")
	sock := adoptSocketDir(t, "tok")
	r := &adoptBridgeRun{frame: string(bridgeAdoptFrame(sock, os.Environ())), done: make(chan error, 1)}
	if r.frame == "" {
		t.Skip("the environment of this host gives a frame over 262144 bytes, so the bridge sends none")
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	r.ln = ln.(*net.UnixListener)
	t.Cleanup(func() { _ = ln.Close() })
	// A bridge that dials no more fails the test at an Accept, not at the time limit.
	if err := r.ln.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	r.stdin, r.stdout = bridgePipes(t)
	ended := make(chan struct{})
	go func() {
		r.done <- runBridge(sock)
		close(ended)
	}()
	// This cleanup runs before the one of bridgePipes, so the bridge has returned
	// before the globals go back.
	t.Cleanup(func() {
		_ = ln.Close()
		for _, c := range r.conns {
			_ = c.Close()
		}
		select {
		case <-ended:
		case <-time.After(10 * time.Second):
			t.Error("runBridge did not return at the end of the test")
		}
	})
	return r
}

// accept takes the next connection of the bridge and its reader.
func (r *adoptBridgeRun) accept(t *testing.T) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := r.ln.Accept()
	if err != nil {
		t.Fatalf("accept connection %d: %v", len(r.conns)+1, err)
	}
	r.conns = append(r.conns, c)
	return c, bufio.NewReader(c)
}

// expectPong writes the reply to the ping on c and waits for it on stdout.
func (r *adoptBridgeRun) expectPong(t *testing.T, c net.Conn) {
	t.Helper()
	const pong = `{"jsonrpc":"2.0","id":7,"result":{"pong":true}}`
	if _, err := io.WriteString(c, pong+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-r.stdout:
		if got != pong {
			t.Errorf("stdout line %q, want %q", got, pong)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no line on stdout")
	}
}

const adoptTestPing = `{"jsonrpc":"2.0","id":7,"method":"server.ping"}` + "\n"

// TestRunBridgeSilentDaemonThirdConnection: a daemon that never answers the frame.
// The first and second connections get the frame, the bridge closes each after the
// wait, and the third gets no frame but the request that the client wrote at the
// start (c9125eb8, measured on Linux, macOS and Windows).
func TestRunBridgeSilentDaemonThirdConnection(t *testing.T) {
	r := startAdoptBridge(t, 300*time.Millisecond)
	if _, err := io.WriteString(r.stdin, adoptTestPing); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 2; n++ {
		c, br := r.accept(t)
		if got := readLineWithin(t, c, br); got != r.frame {
			t.Fatalf("connection %d: first line %q, want the frame", n, got)
		}
		// The bridge closes the connection after the wait, with nothing more on it.
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		rest, err := br.ReadString('\n')
		var te interface{ Timeout() bool }
		if rest != "" || err == nil || errors.As(err, &te) && te.Timeout() {
			t.Fatalf("connection %d: after the frame %q, %v, want the close of the bridge", n, rest, err)
		}
	}
	third, br := r.accept(t)
	if got := readLineWithin(t, third, br); got != adoptTestPing {
		t.Fatalf("third connection: first line %q, want the relayed request %q", got, adoptTestPing)
	}
	r.expectPong(t, third)
}

// TestRunBridgeRetriesAfterClose: the listener closes the first connection at once.
// The bridge dials again, sends the frame there, and relays the request after the
// reply (c9125eb8, measured on Linux, macOS and Windows).
func TestRunBridgeRetriesAfterClose(t *testing.T) {
	// A wait of 1 s, so a slow race run still gets the reply in time.
	r := startAdoptBridge(t, time.Second)
	if _, err := io.WriteString(r.stdin, adoptTestPing); err != nil {
		t.Fatal(err)
	}
	first, _ := r.accept(t)
	_ = first.Close()
	second, br := r.accept(t)
	if got := readLineWithin(t, second, br); got != r.frame {
		t.Fatalf("second connection: first line %q, want the frame", got)
	}
	if _, err := io.WriteString(second, `{"jsonrpc":"2.0","id":"bridge","result":{"ok":true}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if got := readLineWithin(t, second, br); got != adoptTestPing {
		t.Fatalf("second connection: line after the reply %q, want %q", got, adoptTestPing)
	}
	r.expectPong(t, second)
}

// TestRunBridgeSecondCloseThirdConnection: the listener closes the first and the
// second connection at once. The third gets no frame but the request of the client
// (claustrum's own, not measured: the rows close only the first connection).
func TestRunBridgeSecondCloseThirdConnection(t *testing.T) {
	r := startAdoptBridge(t, time.Second)
	if _, err := io.WriteString(r.stdin, adoptTestPing); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		c, _ := r.accept(t)
		_ = c.Close()
	}
	third, br := r.accept(t)
	if got := readLineWithin(t, third, br); got != adoptTestPing {
		t.Fatalf("third connection: first line %q, want the relayed request %q", got, adoptTestPing)
	}
	r.expectPong(t, third)
}

// TestRunBridgeDaemonNeverReads: a daemon that accepts and never reads. The frame
// is larger than the socket buffer, so its write blocks. The wait covers the write,
// and the bridge still reaches the third connection (claustrum's own, not measured).
func TestRunBridgeDaemonNeverReads(t *testing.T) {
	// 220000 bytes is over the default send buffer of Linux (212992) and macOS, and
	// leaves room for a host environment of about 40 KB.
	t.Setenv("CLAUSTRUM_TEST_BIG", strings.Repeat("v", 220000))
	r := startAdoptBridge(t, 300*time.Millisecond)
	if _, err := io.WriteString(r.stdin, adoptTestPing); err != nil {
		t.Fatal(err)
	}
	r.accept(t)
	r.accept(t)
	third, br := r.accept(t)
	if got := readLineWithin(t, third, br); got != adoptTestPing {
		t.Fatalf("third connection: first line %q, want the relayed request %q", got, adoptTestPing)
	}
	r.expectPong(t, third)
}
