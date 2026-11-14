package conn

import (
	"errors"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"
)

// CloseWhenIdle closes a connection that has gone silent for idleTimeout, matching
// the 5-minute idle close measured on f6010b97 (tested here with a short timeout).
// Activity on the connection resets the clock.
func TestCloseWhenIdle(t *testing.T) {
	t.Run("idle_connection_is_closed", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c2.Close()
		ac := NewActivityConn(c1)
		ac.shown.Store(true)
		shutdown := make(chan struct{})
		done := make(chan struct{})
		returned := make(chan struct{})
		buf := captureLogBuf(t)
		go func() { CloseWhenIdle(ac, 80*time.Millisecond, shutdown, done); close(returned) }()

		select {
		case <-returned: // watcher closed the conn and exited
		case <-time.After(2 * time.Second):
			close(done)
			t.Fatal("idle connection was not closed within 2s")
		}
		if _, err := ac.Read(make([]byte, 1)); err == nil {
			t.Error("connection was not actually closed")
		}
		// The line of the reference (`89cb6289`, measured on Linux): "[Server] closing connection
		// idle 5m0s in both directions: @". Claustrum quotes the address (claustrum's own, not
		// measured). The peer text of a net.Pipe is "pipe".
		if got := buf.String(); !regexp.MustCompile(`\[Server\] closing connection idle \d+s in both directions: "pipe"\n$`).MatchString(got) {
			t.Errorf("log = %q, want the idle-close line", got)
		}
	})

	// Claustrum's own, not measured: a connection with no token mark is closed
	// with no line.
	t.Run("idle_connection_without_the_token_mark_closes_silently", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c2.Close()
		ac := NewActivityConn(c1)
		shutdown := make(chan struct{})
		done := make(chan struct{})
		returned := make(chan struct{})
		buf := captureLogBuf(t)
		go func() { CloseWhenIdle(ac, 80*time.Millisecond, shutdown, done); close(returned) }()
		select {
		case <-returned:
		case <-time.After(2 * time.Second):
			close(done)
			t.Fatal("idle connection was not closed within 2s")
		}
		if _, err := ac.Read(make([]byte, 1)); err == nil {
			t.Error("connection was not actually closed")
		}
		if got := buf.String(); strings.Contains(got, "closing connection idle") {
			t.Errorf("log = %q, want no idle-close line", got)
		}
	})

	t.Run("activity_keeps_it_open", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		ac := NewActivityConn(c1)
		shutdown := make(chan struct{})
		done := make(chan struct{})
		defer close(done)
		go CloseWhenIdle(ac, time.Second, shutdown, done)

		// Keep stamping activity every 20 ms for 1.5 s, longer than the idle limit and
		// its poll. The watcher must NOT close it. The 1 s limit leaves a slow sleep of
		// a loaded runner a wide margin.
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			ac.stamp()
			time.Sleep(20 * time.Millisecond)
		}
		// A fresh read deadline that expires proves the conn is still open (a closed
		// pipe would return an error immediately instead of timing out).
		_ = ac.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
		var ne net.Error
		if _, err := ac.Read(make([]byte, 1)); err == nil || !errors.As(err, &ne) || !ne.Timeout() {
			t.Errorf("connection was closed despite steady activity: err=%v", err)
		}
	})
}
