package main

import (
	"net"
	"sync/atomic"
	"time"
)

// idleConnTimeout is how long a connection may go with no read/write activity
// before the daemon closes it. The reference closes a connection after 5 minutes
// with no traffic, measured against f6010b97 on a linux VM. claustrum matches that
// value and keeps it always-on.
const idleConnTimeout = 5 * time.Minute

// activityConn wraps a net.Conn and records the time of the last Read or Write, so
// closeWhenIdle can tell whether the connection has gone silent. On the reference a
// read alone and a write alone each keep a connection open, measured against f6010b97
// on a linux VM.
type activityConn struct {
	net.Conn
	last atomic.Int64 // last activity, unix nanoseconds
	// closedDeliberately is claimed by whichever path decides to end this
	// connection on the daemon's own initiative: the idle watcher below, or a
	// supersede from process.reattach. The claim is a Swap, so exactly one of them
	// logs a reason and closes. Without it a superseded connection is closed once and then logged a
	// second time by the idle watcher, which reads as two separate events.
	closedDeliberately atomic.Bool
}

// claimClose returns true for the first caller only. A later caller learns that
// someone else already owns ending this connection.
func (a *activityConn) claimClose() bool { return !a.closedDeliberately.Swap(true) }

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
// idleTimeout, then returns. It polls at idleTimeout/4 clamped to [1ms, 30s] (at the
// default idleConnTimeout that is 75s → clamped to 30s). The watcher exits when the
// connection closes normally (done) or the daemon shuts down, so it never outlives
// its connection.
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
				// Claim BEFORE logging and closing, so a connection a supersede
				// already ended is not closed or logged twice. This order is
				// claustrum's own and is not probe-measured.
				if !a.claimClose() {
					return
				}
				logInfof("[Server] closing idle connection %s (idle for %s)",
					a.RemoteAddr(), idle.Round(time.Second))
				_ = a.Close()
				return
			}
		}
	}
}
