package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// splitEnd cuts the end member off one reply or stream line (no LF). It returns
// the plain line the daemon marshaled, and the at and len values of the member.
func splitEnd(line []byte) (plain []byte, at, ln int64, ok bool) {
	const key = `,"end":{"at":`
	i := bytes.LastIndex(line, []byte(key))
	if i < 0 || !bytes.HasSuffix(line, []byte("}}")) {
		return nil, 0, 0, false
	}
	rest := string(line[i+len(key) : len(line)-2])
	var a, l int64
	if _, err := fmt.Sscanf(rest, "%d,\"len\":%d", &a, &l); err != nil {
		return nil, 0, 0, false
	}
	if strconv.FormatInt(a, 10)+`,"len":`+strconv.FormatInt(l, 10) != rest {
		return nil, 0, 0, false
	}
	plain = append(append([]byte(nil), line[:i]...), '}')
	return plain, a, l, true
}

func TestStampEndMeasuredShape(t *testing.T) {
	plain := `{"jsonrpc":"2.0","id":1,"result":{"pong":true}}`
	in := []byte(plain + "\n")
	keep := string(in)
	got := string(stampEnd(in, 0))
	want := `{"jsonrpc":"2.0","id":1,"result":{"pong":true},"end":{"at":0,"len":46}}` + "\n"
	if got != want {
		t.Errorf("stampEnd = %q, want %q", got, want)
	}
	if string(in) != keep {
		t.Errorf("stampEnd changed its input: %q", in)
	}
	stream := `{"type":"stream","processId":"f1","stream":"stdout","seq":1,"data":"Z290OmhpCg=="}` + "\n"
	got = string(stampEnd([]byte(stream), 174))
	want = `{"type":"stream","processId":"f1","stream":"stdout","seq":1,"data":"Z290OmhpCg==","end":{"at":174,"len":81}}` + "\n"
	if got != want {
		t.Errorf("stampEnd stream = %q, want %q", got, want)
	}
}

func TestSplitEndRoundTrip(t *testing.T) {
	in := []byte(`{"a":{"b":1}}` + "\n")
	stamped := stampEnd(in, 12)
	line := bytes.TrimSuffix(stamped, []byte("\n"))
	plain, at, ln, ok := splitEnd(line)
	if !ok || at != 12 || ln != int64(len(in)-2) || string(plain) != `{"a":{"b":1}}` {
		t.Errorf("splitEnd = %q %d %d %v", plain, at, ln, ok)
	}
	if _, _, _, ok := splitEnd([]byte(`{"a":1}`)); ok {
		t.Error("splitEnd accepted a line with no end member")
	}
}

// endPipeConn returns a conn that writes into a buffer that the test reads, and a
// function that waits for n lines and gives the bytes written so far.
func endPipeConn(t *testing.T) (*conn, func(int) string) {
	t.Helper()
	client, srv := net.Pipe()
	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := srv.Read(b)
			mu.Lock()
			buf.Write(b[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { client.Close(); srv.Close() })
	return &conn{nc: client}, func(lines int) string {
		// The pipe hands over a write before the reader stores it, so poll.
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			s := buf.String()
			mu.Unlock()
			if strings.Count(s, "\n") >= lines || time.Now().After(deadline) {
				return s
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestConnLinesCarryRunningEndCount(t *testing.T) {
	c, gotN := endPipeConn(t)
	if c.writeResponse(pongResult{Pong: true}) {
		t.Fatal("writeResponse failed")
	}
	shared := []byte(`{"type":"stream","processId":"p","stream":"stdout","seq":1,"data":"AA=="}` + "\n")
	keep := string(shared)
	if err := c.writeLine(shared); err != nil {
		t.Fatal(err)
	}
	if string(shared) != keep {
		t.Errorf("writeLine changed the shared slice: %q", shared)
	}
	c.wmu.Lock()
	err := c.writeReplayLocked([]streamFrame{{Type: "stream", ProcessID: "p", Stream: "stderr", Seq: 2}})
	c.wmu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	got := func() string { return gotN(3) }
	// Each at is the byte count of everything before the line, LF and digits included.
	want := `{"pong":true,"end":{"at":0,"len":12}}` + "\n" +
		`{"type":"stream","processId":"p","stream":"stdout","seq":1,"data":"AA==","end":{"at":38,"len":72}}` + "\n"
	if g := got(); !strings.HasPrefix(g, want) {
		t.Fatalf("wire = %q, want prefix %q", g, want)
	}
	lines := strings.Split(strings.TrimSuffix(got(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("wire has %d lines, want 3: %q", len(lines), lines)
	}
	third := lines[2]
	wantAt := int64(len(lines[0]) + 1 + len(lines[1]) + 1)
	if _, at, ln, ok := splitEnd([]byte(third)); !ok || at != wantAt || ln != int64(strings.Index(third, `,"end"`)) {
		t.Errorf("replay line %q: at=%d len=%d ok=%v, want at=%d", third, at, ln, ok, wantAt)
	}
}

func TestEndCountIsPerConnection(t *testing.T) {
	a, gotA := endPipeConn(t)
	b, gotB := endPipeConn(t)
	a.writeResponse(pongResult{Pong: true})
	a.writeResponse(pongResult{Pong: true})
	b.writeResponse(pongResult{Pong: true})
	if want := `{"pong":true,"end":{"at":0,"len":12}}` + "\n" + `{"pong":true,"end":{"at":38,"len":12}}` + "\n"; gotA(2) != want {
		t.Errorf("conn a wire = %q, want %q", gotA(2), want)
	}
	if want := `{"pong":true,"end":{"at":0,"len":12}}` + "\n"; gotB(1) != want {
		t.Errorf("conn b wire = %q, want %q", gotB(1), want)
	}
}

// The measured sequence of one connection: two pings. The second reply starts
// at 72, the byte count of the first line with its LF (build 52d82f0a).
func TestSocketEndMemberPingSequence(t *testing.T) {
	sock := startSocketServer(t)
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	rd := bufio.NewReader(nc)
	for i, at := range []int{0, 72} {
		if _, err := io.WriteString(nc, authed(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`)+"\n"); err != nil {
			t.Fatal(err)
		}
		got, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("ping %d: %v", i, err)
		}
		want := `{"jsonrpc":"2.0","id":1,"result":{"pong":true},"end":{"at":` + strconv.Itoa(at) + `,"len":46}}` + "\n"
		if got != want {
			t.Errorf("ping %d = %q, want %q", i, got, want)
		}
	}
}

// A new connection that reattaches gets its replayed frames and its result with
// the count of that connection (claustrum's own, not measured). The test client
// checks every end member against its own byte count.
func TestSocketEndMemberOnReplayAndStream(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.send(spawnReq(t, 1, "EN", "stdout3"))
	cl.waitExit("EN")
	cl2 := dial(t, sock)
	cl2.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.reattach","params":{"id":"EN","fromSeq":0}}`))
	if frames := cl2.waitExit("EN"); len(frames) == 0 {
		t.Fatal("no replayed frames")
	}
	for _, c := range []*testClient{cl, cl2} {
		c.mu.Lock()
		bad, read := c.endErr, c.read
		c.mu.Unlock()
		if bad != "" || read == 0 {
			t.Errorf("end member check: %q after %d bytes", bad, read)
		}
	}
}
