package main

import (
	"fmt"
	"net"
	"sync/atomic"
)

// The format texts of the connection log, claustrum's own, not measured. A
// test checks the byte length of each.
const (
	fmtNewConn     = "[Server] New connection from: %s"
	fmtConnClosed  = "[Server] Connection closed: %s"
	fmtTokenless   = "[Server] Connection number %d that never showed the token: %s"
	fmtUnauth      = "[Server] Unauthorized request number %d: method=%.64q"
	fmtParseError  = "[Server] Parse error number %d: %v"
	fmtIdleClose   = "[Server] closing connection idle %s in both directions: %q"
	fmtWriteFailed = "[Server] writeResponse: wrote %d/%d bytes, error=%q"
	fmtReplyFailed = "[Server] Failed to write response: %q"
	fmtScannerErr  = "[Server] scanner error on %s: %q"
	fmtProcExited  = "[process.Manager] Process %s exited with code %d%s"
)

// quotedAddr is the remote address of a connection in %q form.
func quotedAddr(nc interface{ RemoteAddr() net.Addr }) string {
	return fmt.Sprintf("%q", nc.RemoteAddr())
}

// The connection log rules, claustrum's own, not measured: the token mark, the
// quoted remote label, and the sampled lines.

// tokenMark is the mark that this connection showed the token. It is set at the
// first request that passes the token check and never goes back. It lives on the
// activity wrapper so the idle watcher reads the same flag. A conn that a test
// builds around a bare pipe keeps its own.
func (c *conn) tokenMark() *atomic.Bool {
	if c.ac != nil {
		return &c.ac.shown
	}
	return &c.bareShown
}

// remoteLabel is the peer address in %q form, the label of every connection line.
func (c *conn) remoteLabel() string { return quotedAddr(c.nc) }

// showToken sets the token mark. The first call logs the New connection line, so
// the line comes before the daemon handles that request. A nil conn does nothing.
func (c *conn) showToken() {
	if c != nil && c.tokenMark().CompareAndSwap(false, true) {
		logInfof(fmtNewConn, c.remoteLabel())
	}
}

// shuttingDown reports that the daemon began its shutdown.
func (c *conn) shuttingDown() bool {
	select {
	case <-c.down:
		return true
	default:
		return false
	}
}

// logSampled applies the sample rule to the count n of a line kind, counted from 1:
// the counts 1 to 10 and every 1000th.
func logSampled(n uint64) bool { return n <= 10 || n%1000 == 0 }
