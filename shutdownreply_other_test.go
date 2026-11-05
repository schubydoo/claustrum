//go:build !windows

package main

import (
	"bufio"
	"io"
	"net"
	"testing"
	"time"
)

// The server.shutdown handler returns its reply as soon as the stop has started. It
// does not wait for the close of the connections. 5fd08069 answers before the close
// in part of the runs (measured). The stand-in teardown here waits for the reply
// for at most 5 s after the shutdown signal. Then it drops the connections. The
// reply must reach the client before that drop.
func TestShutdownReplyIsWrittenBeforeTheClose(t *testing.T) {
	// A long wait makes a reply that waits for the drop miss the 5 s stand-in.
	const want = `{"jsonrpc":"2.0","id":7,"result":{"ok":true},"end":{"at":0,"len":44}}` + "\n"
	s := newTestServer(t)
	s.replyRule = &shutdownReplyRule{awaitsDrop: shutdownReplyAwaitsDrop, wait: time.Minute}
	srv, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	c := &conn{nc: srv}
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	go s.serveConn(c)

	var line string
	read := make(chan struct{})
	go func() {
		line, _ = bufio.NewReader(client).ReadString('\n')
		close(read)
	}()
	dropped := make(chan struct{})
	go func() {
		<-s.shutdown
		select {
		case <-read:
		case <-time.After(5 * time.Second):
		}
		s.dropConns()
		close(dropped)
	}()

	if _, err := io.WriteString(client, `{"jsonrpc":"2.0","id":7,"method":"server.shutdown"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []chan struct{}{dropped, read} {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatal("the shutdown did not finish")
		}
	}
	if line != want {
		t.Errorf("shutdown connection got %q before the close, want %q", line, want)
	}
}

// The Windows rule of the reply runs on Linux too when a test turns it on, so the
// shared lines of server.go get a Linux test.
func TestShutdownReplyWaitsForTheDropWhenTurnedOn(t *testing.T) {
	checkShutdownReplyWaitsForTheDrop(t)
}

func TestShutdownReplyWaitEndsAtItsBoundWhenTurnedOn(t *testing.T) {
	checkShutdownReplyWaitEndsAtItsBound(t)
}
