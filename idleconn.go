package main

import (
	"net"
	"sync/atomic"
	"time"
)

// idleConnTimeout is how long a connection may go with no read/write activity
// before the daemon closes it. 7d193f89 hardcodes 5 minutes in its server
// constructor with no flag or env to change or disable it, so claustrum matches
// that value and keeps it always-on.
const idleConnTimeout = 5 * time.Minute

// activityConn wraps a net.Conn and records the time of the last Read or Write, so
// closeWhenIdle can tell whether the connection has gone silent. Matches 7d193f89,
// which stamps activity on every read and write of the accepted connection.
type activityConn struct {
	net.Conn
	last atomic.Int64 // last activity, unix nanoseconds
}

func newActivityConn(c net.Conn) *activityConn {
	a := &activityConn{Conn: c}
	a.stamp()
	return a
}

func (a *activityConn) stamp() { a.last.Store(time.Now().UnixNano()) }

func (a *activityConn) Read(b []byte) (int, error) {
	n, err := a.Conn.Read(b)
	a.stamp()
	return n, err
}

func (a *activityConn) Write(b []byte) (int, error) {
	n, err := a.Conn.Write(b)
	a.stamp()
	return n, err
}

// idleFor reports how long the connection has been without read/write activity.
func (a *activityConn) idleFor() time.Duration {
	return time.Duration(time.Now().UnixNano() - a.last.Load())
}

// closeWhenIdle closes the connection once it has been idle for the server's
// idleTimeout, then returns. It polls at idleTimeout/4 clamped to [1ms, 30s] — the
// same cadence as 7d193f89 (300s/4 = 75s → clamped to 30s at the default). The
// watcher exits when the connection closes normally (done) or the daemon shuts
// down, so it never outlives its connection.
func (s *server) closeWhenIdle(a *activityConn, done <-chan struct{}) {
	if s.idleTimeout <= 0 {
		return
	}
	poll := min(max(s.idleTimeout/4, time.Millisecond), 30*time.Second)
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-s.shutdown:
			return
		case <-t.C:
			if idle := a.idleFor(); idle >= s.idleTimeout {
				logInfof("[Server] closing idle connection %s (idle for %s)",
					a.RemoteAddr(), idle.Round(time.Second))
				_ = a.Close()
				return
			}
		}
	}
}
