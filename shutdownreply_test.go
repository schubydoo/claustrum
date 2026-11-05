package main

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
)

const shutdownReplyFrame = `{"jsonrpc":"2.0","id":7,"result":{"ok":true},"end":{"at":0,"len":44}}` + "\n"

// startShutdownOnPipe serves one pipe connection, sends server.shutdown on it and
// waits for the shutdown signal. It returns the client end.
func startShutdownOnPipe(t *testing.T, s *server) net.Conn {
	t.Helper()
	srv, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	c := conn.New(srv, nil, 0, nil, nil, nil)
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	go s.serveConn(c)
	if _, err := io.WriteString(client, `{"jsonrpc":"2.0","id":7,"method":"server.shutdown"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.shutdown:
	case <-time.After(5 * time.Second):
		t.Fatal("server.shutdown did not signal the shutdown")
	}
	return client
}

// checkShutdownReplyWaitsForTheDrop: no byte reaches the client before the drop
// starts. After the drop the client reads nothing or the whole frame, then EOF.
func checkShutdownReplyWaitsForTheDrop(t *testing.T) {
	s := newTestServer(t)
	s.replyRule = &shutdownReplyRule{awaitsDrop: true, wait: 5 * time.Second}
	client := startShutdownOnPipe(t, s)

	_ = client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 256)
	if n, err := client.Read(buf); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("before the drop the client read %q (%v), want no bytes", buf[:n], err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	s.dropConns()
	got, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("no EOF after the drop: %v (read %q)", err, got)
	}
	if string(got) != "" && string(got) != shutdownReplyFrame {
		t.Errorf("after the drop the client read %q, want nothing or %q", got, shutdownReplyFrame)
	}
}

// checkShutdownReplyWaitEndsAtItsBound: with no drop the reply goes out once
// shutdownReplyWait passes.
func checkShutdownReplyWaitEndsAtItsBound(t *testing.T) {
	s := newTestServer(t)
	s.replyRule = &shutdownReplyRule{awaitsDrop: true, wait: 20 * time.Millisecond}
	client := startShutdownOnPipe(t, s)

	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len(shutdownReplyFrame))
	if _, err := io.ReadFull(client, buf); err != nil || string(buf) != shutdownReplyFrame {
		t.Errorf("with no drop the client read %q (%v), want %q", buf, err, shutdownReplyFrame)
	}
	s.dropConns()
}

// The start of dropConns releases a reply that waits in awaitDropStart, long before
// the bound. A second dropConns does not close the channel again.
func TestDropConnsReleasesTheShutdownReplyWait(t *testing.T) {
	s := newTestServer(t)
	s.replyRule = &shutdownReplyRule{awaitsDrop: true, wait: time.Minute}
	released := make(chan struct{})
	go func() {
		s.awaitDropStart()
		close(released)
	}()
	s.dropConns()
	s.dropConns()
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("dropConns did not release awaitDropStart")
	}
}
