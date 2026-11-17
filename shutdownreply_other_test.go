//go:build !windows

package main

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

// On Linux and macOS no byte of the server.shutdown reply reaches the client before
// the teardown starts.
func TestShutdownReplyWaitsForTheTeardown(t *testing.T) {
	s := newTestServer(t)
	s.replyRule = &shutdownReplyRule{awaitsDrop: shutdownReplyAwaitsDrop, wait: 5 * time.Second}
	client := startShutdownOnPipe(t, s)

	_ = client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 256)
	if n, err := client.Read(buf); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("before the teardown the client read %q (%v), want no bytes", buf[:n], err)
	}
	s.startTeardown()
	s.dropConns()
}

// After the start of the teardown the reply can win: with the drop held back, the
// client reads the whole frame. The reply does not wait for the drop.
func TestShutdownReplyCanWinTheClose(t *testing.T) {
	s := newTestServer(t)
	s.replyRule = &shutdownReplyRule{awaitsDrop: shutdownReplyAwaitsDrop, wait: time.Minute}
	client := startShutdownOnPipe(t, s)

	s.startTeardown()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len(shutdownReplyFrame))
	if _, err := io.ReadFull(client, buf); err != nil || string(buf) != shutdownReplyFrame {
		t.Fatalf("before the drop the client read %q (%v), want %q", buf, err, shutdownReplyFrame)
	}
	s.dropConns()
	if rest, err := io.ReadAll(client); err != nil || len(rest) != 0 {
		t.Errorf("after the frame the client read %q (%v), want a clean EOF", rest, err)
	}
}

// After the start of the teardown the close can win: a drop before the client reads
// leaves it a clean EOF with no bytes. The pipe holds the write until a read.
func TestShutdownCloseCanWinTheReply(t *testing.T) {
	s := newTestServer(t)
	s.replyRule = &shutdownReplyRule{awaitsDrop: shutdownReplyAwaitsDrop, wait: time.Minute}
	client := startShutdownOnPipe(t, s)

	s.startTeardown()
	s.dropConns()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if got, err := io.ReadAll(client); err != nil || len(got) != 0 {
		t.Errorf("after the drop the client read %q (%v), want a clean EOF with no bytes", got, err)
	}
}

// closeAll releases a reply that waits for the start of the teardown, long before the
// bound.
func TestCloseAllReleasesTheShutdownReplyWait(t *testing.T) {
	s, sock := bootLineServer(t, false)
	s.replyRule = &shutdownReplyRule{awaitsDrop: shutdownReplyAwaitsDrop, wait: time.Minute}
	released := make(chan struct{})
	go func() {
		s.awaitShutdownReplyTurn()
		close(released)
	}()
	s.signalShutdown()
	s.closeAll(sock)
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("closeAll did not release awaitShutdownReplyTurn")
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
