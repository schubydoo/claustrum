package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

// eofParseErrFrame is the 76-byte parse-error frame. Measured against
// f6010b97 and 90fca6e6 on a Linux VM, the reference sent exactly these bytes
// for a line that does not decode into a request.
const eofParseErrFrame = `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error"}}` + "\n"

// eofAuthErrFrame is the -32001 frame for a request with the id id, given as
// raw JSON. Measured against f6010b97 and 90fca6e6 on a Linux VM, the
// reference echoed the id, and sent null for a missing id.
func eofAuthErrFrame(id string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32001,"message":"Unauthorized: invalid or missing auth token"}}` + "\n"
}

const eofPing = `{"jsonrpc":"2.0","id":1,"method":"server.ping","params":{},"auth":"` + testToken + `"}`

// holdDispatch replaces the dispatchRequest seam with one that records each
// request and then blocks until the test ends. A reply from the async dispatch
// path therefore never reaches the wire before the close. What the wire
// carries comes from the read path alone. So a test that passes cannot depend
// on a goroutine winning the race against the close.
func holdDispatch(t *testing.T) (seen func() []string) {
	t.Helper()
	var (
		mu   sync.Mutex
		raws []string
		wg   sync.WaitGroup
	)
	release := make(chan struct{})
	old := dispatchRequest
	dispatchRequest = func(s *server, c *conn, raw []byte, req request) *response {
		wg.Add(1)
		defer wg.Done()
		mu.Lock()
		raws = append(raws, string(raw))
		mu.Unlock()
		<-release
		return old(s, c, raw, req)
	}
	t.Cleanup(func() {
		close(release)
		wg.Wait()
		dispatchRequest = old
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), raws...)
	}
}

// waitSeen polls seen until it holds n requests, or fails after 5 s.
func waitSeen(t *testing.T, seen func() []string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := seen(); len(got) >= n {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("dispatch saw %q, want %d request(s)", seen(), n)
	return nil
}

// eofExchange sends raw on a fresh connection, half-closes the write side
// (SHUT_WR) and returns every byte the daemon writes before it closes.
func eofExchange(t *testing.T, sock, raw string) string {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer nc.Close()
	if _, err := io.WriteString(nc, raw); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := nc.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	return readToEOF(t, nc)
}

func readToEOF(t *testing.T, nc net.Conn) string {
	t.Helper()
	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(nc)
	if err != nil {
		t.Fatalf("read to EOF: %v (got %q)", err, got)
	}
	return string(got)
}

// Case 1: a valid request with no newline, then SHUT_WR. It is dispatched,
// and no parse error is written for it.
func TestSocketEOFUnterminatedValidRequestDispatchesSilently(t *testing.T) {
	seen := holdDispatch(t)
	sock := startSocketServer(t)
	if got := eofExchange(t, sock, eofPing); got != "" {
		t.Errorf("wire = %q, want 0 bytes", got)
	}
	if got := waitSeen(t, seen, 1); got[0] != eofPing {
		t.Errorf("dispatched %q, want the unterminated ping", got[0])
	}
}

// A line that fails the parse check or the auth check, then SHUT_WR at once.
// The error frame reaches the wire on the read path before the close, with or
// without a final newline. Nothing goes to the async dispatch. Each wanted
// frame has the bytes measured on the reference for its class and id.
func TestSocketEOFGateErrorOnReadPath(t *testing.T) {
	const noAuthPing = `{"jsonrpc":"2.0","id":1,"method":"server.ping"}`
	idLine := func(id string) string {
		return `{"jsonrpc":"2.0","id":` + id + `,"method":"server.ping"}`
	}
	cases := []struct{ name, line, want string }{
		{"partial object", `{"jsonrpc":"2.0","id":1,`, eofParseErrFrame},
		{"spaces only", "   ", eofParseErrFrame},
		{"not json", "not json", eofParseErrFrame},
		{"array", "[1]", eofParseErrFrame},
		{"number", "123", eofParseErrFrame},
		{"string", `"x"`, eofParseErrFrame},
		{"trailing CR", "abc\r", eofParseErrFrame},
		{"batch with auth", "[" + eofPing + "]", eofParseErrFrame},
		{"batch without auth", "[" + noAuthPing + "]", eofParseErrFrame},
		{"null", "null", eofAuthErrFrame("null")},
		{"empty object", "{}", eofAuthErrFrame("null")},
		{"version only", `{"jsonrpc":"2.0"}`, eofAuthErrFrame("null")},
		{"missing auth", noAuthPing, eofAuthErrFrame("1")},
		{"wrong auth", `{"jsonrpc":"2.0","id":1,"method":"server.ping","params":{},"auth":"wrong"}`, eofAuthErrFrame("1")},
		{"string id", idLine(`"a"`), eofAuthErrFrame(`"a"`)},
		{"number id", idLine("7"), eofAuthErrFrame("7")},
		{"null id", idLine("null"), eofAuthErrFrame("null")},
		{"object id", idLine(`{"a":1}`), eofAuthErrFrame(`{"a":1}`)},
		{"missing id", `{"jsonrpc":"2.0","method":"server.ping"}`, eofAuthErrFrame("null")},
		{"auth before version", `{"jsonrpc":"1.0","id":1,"method":"server.ping"}`, eofAuthErrFrame("1")},
	}
	for _, tc := range cases {
		for _, end := range []struct{ name, tail string }{{"newline", "\n"}, {"no newline", ""}} {
			t.Run(tc.name+"/"+end.name, func(t *testing.T) {
				seen := holdDispatch(t)
				sock := startSocketServer(t)
				if got := eofExchange(t, sock, tc.line+end.tail); got != tc.want {
					t.Errorf("wire = %q, want exactly %q", got, tc.want)
				}
				if got := seen(); len(got) != 0 {
					t.Errorf("the line also went to async dispatch: %q", got)
				}
			})
		}
	}
}

// A tail of only a carriage return, then SHUT_WR. The scanner reads it as a
// blank line, so the wire carries 0 bytes and nothing is dispatched. Measured
// against f6010b97 and 90fca6e6 on a Linux VM, the reference sent 0 bytes too.
func TestSocketEOFCarriageReturnTailIsSilent(t *testing.T) {
	seen := holdDispatch(t)
	sock := startSocketServer(t)
	if got := eofExchange(t, sock, "\r"); got != "" {
		t.Errorf("wire = %q, want 0 bytes", got)
	}
	if got := seen(); len(got) != 0 {
		t.Errorf("dispatched %q, want nothing", got)
	}
}

// Control: a line that passes the parse and auth checks stays on the async
// dispatch path, with or without a final newline. That covers a version
// error, an unknown namespace, and the unauthenticated server.shutdown. Under
// the held seam its reply cannot reach the wire before the close, so the wire
// carries 0 bytes. If the read path wrote any of these replies, this test
// fails.
func TestSocketEOFLaterClassStaysAsync(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"version", `{"jsonrpc":"1.0","id":1,"method":"server.ping","params":{},"auth":"` + testToken + `"}`},
		{"unknown namespace", `{"jsonrpc":"2.0","id":1,"method":"no.such","params":{},"auth":"` + testToken + `"}`},
		{"shutdown without auth", `{"jsonrpc":"2.0","id":1,"method":"server.shutdown","params":{}}`},
	} {
		for _, end := range []struct{ name, tail string }{{"newline", "\n"}, {"no newline", ""}} {
			t.Run(tc.name+"/"+end.name, func(t *testing.T) {
				seen := holdDispatch(t)
				sock := startSocketServer(t)
				if got := eofExchange(t, sock, tc.line+end.tail); got != "" {
					t.Errorf("wire = %q, want 0 bytes under the held seam", got)
				}
				if got := waitSeen(t, seen, 1); len(got) != 1 || got[0] != tc.line {
					t.Errorf("dispatched %q, want the line once", got)
				}
			})
		}
	}
}

// Case 4: a valid line, then a partial line, then SHUT_WR. claustrum does not
// pin the order of the two replies. Under the held seam the ping reply cannot
// reach the wire before the close. So the wire carries the parse-error frame
// alone.
func TestSocketEOFParseErrorAfterValidLine(t *testing.T) {
	seen := holdDispatch(t)
	sock := startSocketServer(t)
	if got := eofExchange(t, sock, eofPing+"\n"+`{"jsonrpc":"2.0","id":2,`); got != eofParseErrFrame {
		t.Errorf("wire = %q, want exactly %q", got, eofParseErrFrame)
	}
	if raws := waitSeen(t, seen, 1); len(raws) != 1 || raws[0] != eofPing {
		t.Errorf("dispatched %q, want the ping line only", raws)
	}
}

// O1: a request that blocks does not hold back the auth error for a later
// line on the same open connection. The held seam blocks the first request.
// Measured against f6010b97 and 90fca6e6 on a Linux VM, the reference sent
// the later auth error while the earlier request was still blocked.
func TestSocketAuthErrorNotHeldBehindBlockedRequest(t *testing.T) {
	seen := holdDispatch(t)
	sock := startSocketServer(t)
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer nc.Close()
	if _, err := io.WriteString(nc, eofPing+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitSeen(t, seen, 1)
	if _, err := io.WriteString(nc, `{"jsonrpc":"2.0","id":2,"method":"server.ping","params":{},"auth":"wrong"}`+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := eofAuthErrFrame("2")
	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(nc, buf); err != nil || string(buf) != want {
		t.Fatalf("frame = %q (%v), want %q", buf, err, want)
	}
}

// Case 5 and N4: bytes with no newline, and the socket kept open and silent.
// Nothing is read as a line until the idle close. Then a valid request is
// dispatched, and a bad tail gets its parse error on the read path. That
// write comes after the close, so it fails. The wire carries 0 bytes in both.
// Measured against f6010b97 and 90fca6e6 on a Linux VM, the reference sent 0
// bytes in both cases too.
func TestSocketEOFUnterminatedAtIdleClose(t *testing.T) {
	for _, tc := range []struct {
		name, send string
		dispatched bool
	}{
		{"valid ping", eofPing, true},
		{"partial object", `{"jsonrpc":"2.0","id":1,`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := holdDispatch(t)
			s, sock := newRunningServer(t)
			nc, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer nc.Close()
			var c *conn
			deadline := time.Now().Add(5 * time.Second)
			for c == nil && time.Now().Before(deadline) {
				s.mu.Lock()
				for k := range s.conns {
					c = k
				}
				s.mu.Unlock()
				time.Sleep(time.Millisecond)
			}
			if c == nil {
				t.Fatal("the daemon did not register the connection")
			}
			s.idleTimeout = 300 * time.Millisecond
			go s.closeWhenIdle(c.ac, nil)
			if _, err := io.WriteString(nc, tc.send); err != nil {
				t.Fatalf("write: %v", err)
			}
			time.Sleep(50 * time.Millisecond)
			if got := seen(); len(got) != 0 {
				t.Fatalf("dispatched %q before the idle close, want nothing yet", got)
			}
			if got := readToEOF(t, nc); got != "" {
				t.Errorf("wire = %q, want 0 bytes", got)
			}
			if !tc.dispatched {
				if got := seen(); len(got) != 0 {
					t.Errorf("dispatched %q, want nothing", got)
				}
				return
			}
			if got := waitSeen(t, seen, 1); got[0] != eofPing {
				t.Errorf("dispatched %q at the idle close, want the ping", got[0])
			}
		})
	}
}

// Case 6 (control): a newline-terminated bad line on an open connection gets
// the parse-error frame at once, then EOF after SHUT_WR.
func TestSocketEOFTerminatedBadLineControl(t *testing.T) {
	sock := startSocketServer(t)
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer nc.Close()
	if _, err := io.WriteString(nc, "not json\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len(eofParseErrFrame))
	if _, err := io.ReadFull(nc, buf); err != nil || string(buf) != eofParseErrFrame {
		t.Fatalf("frame = %q (%v), want %q", buf, err, eofParseErrFrame)
	}
	if err := nc.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	if rest := readToEOF(t, nc); rest != "" {
		t.Errorf("bytes after the frame = %q, want none", rest)
	}
}

// afunixEOFGap waits before a test closes a socket or stdin just after it
// wrote. It waits on Windows only. Measured on a Windows VM, an AF_UNIX
// reader got the bytes and then missed the EOF that came right after them.
// It missed 0.1% to 1% of EOFs from shutdown(SD_SEND), and 3% to 4% from a
// full close. A plain blocking WSARecv loop missed them too, so the loss is
// below Go. A new read then returned the EOF. With a 1 or 2 ms wait before
// the close, 0 EOFs in 3000 to 5000 runs were missed. A reference bridge run
// also hung on that VM. So the gap keeps these tests on the behavior of the
// bridge, not on the Windows race.
func afunixEOFGap() {
	if runtime.GOOS == "windows" {
		time.Sleep(50 * time.Millisecond)
	}
}

// runBridgeWith runs runBridge against sock with stdin fed from in. It closes
// stdin after the write and returns what the bridge put on stdout. It fails
// if runBridge does not return, or returns an error.
func runBridgeWith(t *testing.T, sock, in string) string {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer outR.Close()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	done := make(chan error, 1)
	go func() { done <- runBridge(sock) }()
	// runBridge reads the globals once, before it relays. Restoring them
	// after it returns keeps the swap out of any goroutine it leaves behind.
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	out := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(outR)
		out <- string(b)
	}()
	if _, err := io.WriteString(inW, in); err != nil {
		t.Fatal(err)
	}
	afunixEOFGap()
	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runBridge = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runBridge did not return after the daemon closed")
	}
	_ = outW.Close()
	return <-out
}

// Cases 7p and N5: the bridge keeps relaying after stdin EOF. So the frame the
// daemon writes on the read path reaches stdout, and the bridge then returns
// nil (exit 0). Measured against f6010b97 and 90fca6e6 on a Linux VM, the
// reference bridge printed the same bytes for these tails.
func TestBridgeEOFRelaysFinalGateError(t *testing.T) {
	for _, tc := range []struct{ name, send, want string }{
		{"partial object", `{"jsonrpc":"2.0","id":1,`, eofParseErrFrame},
		{"array", "[1]", eofParseErrFrame},
		{"null", "null", eofAuthErrFrame("null")},
		{"empty object", "{}", eofAuthErrFrame("null")},
		{"version only", `{"jsonrpc":"2.0"}`, eofAuthErrFrame("null")},
		{"carriage return only", "\r", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			holdDispatch(t)
			sock := startSocketServer(t)
			if got := runBridgeWith(t, sock, tc.send); got != tc.want {
				t.Errorf("bridge stdout = %q, want exactly %q", got, tc.want)
			}
		})
	}
}

// Cases 7 and 7nl: a valid ping through the bridge, with and without the
// newline. Under the held seam the ping reply cannot reach the wire before
// the close. So stdout stays empty.
func TestBridgeEOFValidPingLeavesStdoutEmpty(t *testing.T) {
	for _, tc := range []struct{ name, send string }{
		{"unterminated", eofPing},
		{"newline", eofPing + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := holdDispatch(t)
			sock := startSocketServer(t)
			if got := runBridgeWith(t, sock, tc.send); got != "" {
				t.Errorf("bridge stdout = %q, want 0 bytes", got)
			}
			if got := waitSeen(t, seen, 1); got[0] != eofPing {
				t.Errorf("dispatched %q, want the ping", got[0])
			}
		})
	}
}

// N6: the daemon closes while the bridge's stdin is still open. The bridge
// prints the frame that arrived before the close and returns nil (exit 0).
// Measured against f6010b97 and 90fca6e6 on a Linux VM, the reference bridge
// did the same. The daemon closed by SIGKILL or at the idle close there.
func TestBridgeExitsWhenDaemonClosesWithStdinOpen(t *testing.T) {
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := dir + string(os.PathSeparator) + "s.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	const frame = "last frame\n"
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = io.WriteString(c, frame)
		afunixEOFGap()
		_ = c.Close()
	}()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// Close stdin only when the test ends, after runBridge has returned.
	defer inR.Close()
	defer inW.Close()
	defer outR.Close()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	done := make(chan error, 1)
	go func() { done <- runBridge(sock) }()
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	out := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(outR)
		out <- string(b)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runBridge = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runBridge did not return after the daemon closed")
	}
	_ = outW.Close()
	if got := <-out; got != frame {
		t.Errorf("bridge stdout = %q, want %q", got, frame)
	}
}

// The bridge half-closes at stdin EOF: the peer reads EOF while the bridge
// still reads. The peer then answers, and that answer reaches stdout. A bridge
// that closes fully loses the answer. A bridge that never half-closes leaves
// the peer waiting, and runBridgeWith times out.
func TestBridgeHalfClosesAndDrains(t *testing.T) {
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := dir + string(os.PathSeparator) + "s.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	const late = "late frame\n"
	peerGot := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			peerGot <- ""
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(c)
		peerGot <- string(b)
		_, _ = io.WriteString(c, late)
		afunixEOFGap()
	}()
	if got := runBridgeWith(t, sock, "abc"); got != late {
		t.Errorf("bridge stdout = %q, want %q", got, late)
	}
	if got := <-peerGot; got != "abc" {
		t.Errorf("peer read %q before EOF, want %q", got, "abc")
	}
}

// Without a CloseWrite method, closeWriteOrClose closes the connection fully,
// so the peer still reads EOF.
func TestCloseWriteOrCloseFallsBackToClose(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	got := make(chan error, 1)
	go func() {
		_, err := b.Read(make([]byte, 1))
		got <- err
	}()
	closeWriteOrClose(a)
	select {
	case err := <-got:
		if err != io.EOF {
			t.Errorf("peer read error = %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("peer did not read EOF")
	}
	if _, err := a.Write([]byte("x")); err == nil || !bytes.Contains([]byte(err.Error()), []byte("closed")) {
		t.Errorf("write after fallback = %v, want a closed-pipe error", err)
	}
}
