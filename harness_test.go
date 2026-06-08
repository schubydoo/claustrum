package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// updateGolden regenerates the testdata/*.golden.json fixtures from the live
// daemon output instead of comparing against them. Run: `go test -run Socket -update`.
var updateGolden = flag.Bool("update", false, "update golden fixtures in testdata/")

// startSocketServer boots a real AF_UNIX daemon on a temp socket using the
// production per-connection path (serveConn -> dispatch -> writeJSON / emit)
// with a test-controlled accept loop. Unlike (*server).run it installs no
// signal handlers, and its cleanup never calls os.Exit, so it is safe under
// `go test`. The returned socket path is ready to Dial.
func newRunningServer(t *testing.T) (*server, string) {
	t.Helper()
	// A short socket path on purpose: t.TempDir() embeds the (long) test name,
	// and the full AF_UNIX path must stay under the macOS sun_path limit
	// (~104 bytes) or net.Listen fails with "invalid argument".
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	s := &server{
		token:    testToken,
		ln:       ln,
		procs:    newProcManager(),
		conns:    make(map[*conn]struct{}),
		shutdown: make(chan struct{}),
	}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return // listener closed by cleanup
			}
			c := &conn{nc: nc}
			s.mu.Lock()
			s.conns[c] = struct{}{}
			s.mu.Unlock()
			go s.serveConn(c)
		}
	}()
	// Mimic the real teardown's connection close on server.shutdown (minus the
	// os.Exit) so a client that waits for EOF after sending server.shutdown
	// (e.g. runStop) actually unblocks. Without this the harness leaves the
	// connection open and such a client hangs forever.
	stop := make(chan struct{})
	go func() {
		select {
		case <-s.shutdown:
			s.mu.Lock()
			for c := range s.conns {
				_ = c.nc.Close()
			}
			s.mu.Unlock()
			_ = ln.Close()
		case <-stop:
		}
	}()
	t.Cleanup(func() {
		close(stop)
		ln.Close()
		s.procs.killAll()
	})
	return s, sock
}

// startSocketServer is newRunningServer for tests that only need the socket path.
func startSocketServer(t *testing.T) string {
	t.Helper()
	_, sock := newRunningServer(t)
	return sock
}

// testClient is a newline-framed JSON-RPC client. Its read loop demuxes the two
// kinds of inbound line: replies (carry an "id") accumulate in resp; stream
// notifications ("type":"stream") accumulate in frames — exactly the split the
// reference battery makes.
type testClient struct {
	t    *testing.T
	nc   net.Conn
	mu   sync.Mutex
	resp []json.RawMessage
	fr   []streamFrame
}

func dial(t *testing.T, sock string) *testClient {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	cl := &testClient{t: t, nc: nc}
	go cl.readLoop()
	t.Cleanup(func() { nc.Close() })
	return cl
}

func (cl *testClient) readLoop() {
	sc := bufio.NewScanner(cl.nc)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		if len(line) == 0 {
			continue
		}
		// Discriminator: stream frames carry "type":"stream"; replies carry "id".
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(line, &probe)
		cl.mu.Lock()
		if probe.Type == "stream" {
			var f streamFrame
			if json.Unmarshal(line, &f) == nil {
				cl.fr = append(cl.fr, f)
			}
		} else {
			cl.resp = append(cl.resp, line)
		}
		cl.mu.Unlock()
	}
}

// send writes one raw request line (auth must already be embedded where needed).
func (cl *testClient) send(line string) {
	cl.t.Helper()
	if _, err := cl.nc.Write([]byte(line + "\n")); err != nil {
		cl.t.Fatalf("write: %v", err)
	}
}

// authed embeds the valid test token into a request line built from a method +
// optional params/id fields. Callers pass the full object minus auth.
func authed(body string) string {
	// body is a JSON object WITHOUT the closing brace's auth; inject before the last '}'.
	return body[:len(body)-1] + `,"auth":"` + testToken + `"}`
}

// wait polls cond (called while holding cl.mu) until true or a 5s deadline.
func (cl *testClient) wait(cond func() bool) {
	cl.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cl.mu.Lock()
		ok := cond()
		cl.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	cl.t.Fatal("timeout waiting for expected responses/frames")
}

// call sends one request and blocks until its reply arrives, returning it. It
// serializes the round trip so state-dependent calls (e.g. a git op that
// mutates the repo) observe a deterministic order — unlike the concurrent
// dispatch the response battery deliberately exercises.
func (cl *testClient) call(line string) json.RawMessage {
	cl.t.Helper()
	cl.mu.Lock()
	n := len(cl.resp)
	cl.mu.Unlock()
	cl.send(line)
	cl.wait(func() bool { return len(cl.resp) > n })
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.resp[len(cl.resp)-1]
}

func (cl *testClient) waitResponses(n int) []json.RawMessage {
	cl.wait(func() bool { return len(cl.resp) >= n })
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return append([]json.RawMessage(nil), cl.resp...)
}

// waitExit blocks until an "exit" frame for processID arrives, then returns all
// frames for that process in arrival order.
func (cl *testClient) waitExit(processID string) []streamFrame {
	cl.wait(func() bool {
		for _, f := range cl.fr {
			if f.ProcessID == processID && f.Stream == "exit" {
				return true
			}
		}
		return false
	})
	cl.mu.Lock()
	defer cl.mu.Unlock()
	var out []streamFrame
	for _, f := range cl.fr {
		if f.ProcessID == processID {
			out = append(out, f)
		}
	}
	return out
}

// streamBytes concatenates (by seq) the base64-decoded data of every frame on
// the given stream, yielding the child's full stdout/stderr regardless of how
// the daemon chunked it into frames.
func streamBytes(t *testing.T, frames []streamFrame, stream string) string {
	t.Helper()
	var sb []byte
	for _, f := range frames {
		if f.Stream != stream {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			t.Fatalf("frame data not base64: %v", err)
		}
		sb = append(sb, b...)
	}
	return string(sb)
}
