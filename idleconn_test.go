package main

import (
	"errors"
	"net"
	"testing"
	"time"
)

// closeWhenIdle closes a connection that has gone silent for idleTimeout, matching
// 7d193f89's 5-minute idle close (tested here with a short timeout). Activity on the
// connection resets the clock.
func TestCloseWhenIdle(t *testing.T) {
	t.Run("idle_connection_is_closed", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c2.Close()
		ac := newActivityConn(c1)
		s := &server{shutdown: make(chan struct{}), idleTimeout: 80 * time.Millisecond}
		done := make(chan struct{})
		returned := make(chan struct{})
		go func() { s.closeWhenIdle(ac, done); close(returned) }()

		select {
		case <-returned: // watcher closed the conn and exited
		case <-time.After(2 * time.Second):
			close(done)
			t.Fatal("idle connection was not closed within 2s")
		}
		if _, err := ac.Read(make([]byte, 1)); err == nil {
			t.Error("connection was not actually closed")
		}
	})

	t.Run("activity_keeps_it_open", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		ac := newActivityConn(c1)
		s := &server{shutdown: make(chan struct{}), idleTimeout: 120 * time.Millisecond}
		done := make(chan struct{})
		defer close(done)
		go s.closeWhenIdle(ac, done)

		// Keep stamping activity for ~300ms; the watcher must NOT close it.
		deadline := time.Now().Add(300 * time.Millisecond)
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
