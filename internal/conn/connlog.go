package conn

import (
	"fmt"
	"net"
	"sync/atomic"

	"github.com/schubydoo/claustrum/internal/logging"
)

// The format texts of the connection log that this package writes, claustrum's
// own, not measured. A test checks the byte length of each.
const (
	FmtNewConn     = "[Server] New connection from: %s"
	FmtIdleClose   = "[Server] closing connection idle %s in both directions: %q"
	FmtWriteFailed = "[Server] writeResponse: wrote %d/%d bytes, error=%q"
	FmtReplyFailed = "[Server] Failed to write response: %q"
)

// quotedAddr is the remote address of a connection in %q form.
func quotedAddr(nc interface{ RemoteAddr() net.Addr }) string {
	return fmt.Sprintf("%q", nc.RemoteAddr())
}

// The connection log rules, claustrum's own, not measured: the token mark and the
// quoted remote label.

// TokenMark is the mark that this connection showed the token. It is set at the
// first request that passes the token check and never goes back. It lives on the
// activity wrapper so the idle watcher reads the same flag. A conn that a test
// builds around a bare pipe keeps its own.
func (c *Conn) TokenMark() *atomic.Bool {
	if c.ac != nil {
		return &c.ac.shown
	}
	return &c.bareShown
}

// RemoteLabel is the peer address in %q form, the label of every connection line.
func (c *Conn) RemoteLabel() string { return quotedAddr(c.nc) }

// ShowToken sets the token mark. The first call logs the New connection line, so
// the line comes before the daemon handles that request. A nil conn does nothing.
func (c *Conn) ShowToken() {
	if c != nil && c.TokenMark().CompareAndSwap(false, true) {
		logging.Infof(FmtNewConn, c.RemoteLabel())
	}
}

// ShuttingDown reports that the daemon began its shutdown.
func (c *Conn) ShuttingDown() bool {
	select {
	case <-c.down:
		return true
	default:
		return false
	}
}
