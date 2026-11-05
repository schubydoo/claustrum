package conn

import (
	"net"
	"sync/atomic"
	"time"

	"github.com/schubydoo/claustrum/internal/logging"
)

// IdleConnTimeout is how long a connection may go with no read/write activity
// before the daemon closes it. The reference closes a connection after 5 minutes
// with no traffic, measured against f6010b97 on Linux. claustrum matches that
// value and keeps it always-on.
const IdleConnTimeout = 5 * time.Minute

// ActivityConn wraps a net.Conn and records the time of the last Read or Write, so
// CloseWhenIdle can tell whether the connection has gone silent. On the reference a
// read alone and a write alone each keep a connection open, measured against f6010b97
// on Linux.
type ActivityConn struct {
	net.Conn
	last atomic.Int64 // last activity, unix nanoseconds
	// closedDeliberately is claimed by whichever path decides to end this
	// connection on the daemon's own initiative: the idle watcher below, or a
	// supersede from process.reattach. The claim is a Swap, so exactly one of them
	// logs a reason and closes. Without it a superseded connection is closed once and then logged a
	// second time by the idle watcher, which reads as two separate events.
	closedDeliberately atomic.Bool
	// shown is the token mark of the connection (Conn.TokenMark).
	shown atomic.Bool
}

// ClaimClose returns true for the first caller only. A later caller learns that
// someone else already owns ending this connection.
func (a *ActivityConn) ClaimClose() bool { return !a.closedDeliberately.Swap(true) }

// NewActivityConn wraps c and stamps the time of its start.
func NewActivityConn(c net.Conn) *ActivityConn {
	a := &ActivityConn{Conn: c}
	a.stamp()
	return a
}

func (a *ActivityConn) stamp() { a.last.Store(time.Now().UnixNano()) }

func (a *ActivityConn) Read(b []byte) (int, error) {
	n, err := a.Conn.Read(b)
	a.stamp()
	return n, err
}

func (a *ActivityConn) Write(b []byte) (int, error) {
	n, err := a.Conn.Write(b)
	a.stamp()
	return n, err
}

// idleFor reports how long the connection has been without read/write activity.
func (a *ActivityConn) idleFor() time.Duration {
	return time.Duration(time.Now().UnixNano() - a.last.Load())
}

// CloseWhenIdle closes the connection once it has been idle for the server's
// idleTimeout, then returns. It polls at idleTimeout/4 clamped to [1ms, 30s] (at the
// default IdleConnTimeout that is 75s → clamped to 30s). The watcher exits when the
// connection closes normally (done) or the daemon shuts down, so it never outlives
// its connection.
func CloseWhenIdle(a *ActivityConn, idleTimeout time.Duration, shutdown, done <-chan struct{}) {
	if idleTimeout <= 0 {
		return
	}
	poll := min(max(idleTimeout/4, time.Millisecond), 30*time.Second)
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-shutdown:
			return
		case <-t.C:
			if idle := a.idleFor(); idle >= idleTimeout {
				// Claim BEFORE logging and closing, so a connection a supersede
				// already ended is not closed or logged twice. This order is
				// claustrum's own and is not probe-measured.
				if !a.ClaimClose() {
					return
				}
				// The text is the reference's, measured on Linux against 89cb6289
				// (a connection idle for 5 minutes). Claustrum's own, not
				// measured: the address is quoted, and a connection that never
				// showed the token logs no line.
				if a.shown.Load() {
					logging.Infof(FmtIdleClose,
						idle.Round(time.Second), a.RemoteAddr())
				}
				_ = a.Close()
				return
			}
		}
	}
}
