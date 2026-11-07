// Package conn is one client connection of the daemon: its writes, the end
// member of each line, the stdin order and the idle watcher.
package conn

import (
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"

	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/wirelog"
)

// Conn is one connected client. The write mutex serializes the interleaving of
// JSON-RPC responses and id-less stream notifications on the same socket.
type Conn struct {
	nc net.Conn
	// ac is nc again, typed, when the accept loop wrapped it. supersede needs the
	// deliberate-close flag, and a test that builds a conn around a bare pipe
	// leaves this nil. Set once at accept and never mutated.
	ac     *ActivityConn
	wmu    sync.Mutex
	closed bool
	// sent is the count of bytes written on this connection. It is guarded by wmu
	// and gives the "at" of the end member of each line.
	sent int64

	// bareShown is the token mark of a conn that has no ac (see TokenMark).
	bareShown atomic.Bool
	// down is the shutdown channel of the daemon, nil for a conn that a test builds.
	// A closed channel silences the write and read error lines (claustrum's own, not measured).
	down <-chan struct{}

	// sentStop is set under the lock of the server when this connection sent server.shutdown.
	sentStop bool

	// done is closed when serveConn returns, so the per-connection idle watcher
	// (CloseWhenIdle) exits promptly instead of lingering until the idle timeout.
	done chan struct{}

	// id correlates this connection's frames in a -wire-log capture. It has no
	// wire meaning and is never sent to the client.
	id uint64
	// wlog is the server's recorder (nil when off), held here so the write paths
	// can record without reaching back through the server.
	wlog *wirelog.Log

	// process.stdin is the one method whose ARRIVAL ORDER is part of its meaning:
	// the chunks are a byte stream, and delivering them out of order corrupts it.
	// Everything else on a connection stays concurrent (the reference dispatches
	// concurrently and replies can return out of order — that is contract), but
	// stdin requests take a ticket in read order and are admitted one at a time.
	//
	// Measured at 5db5e4a: 20 stdin chunks pipelined back-to-back on one
	// connection arrive in order on the reference and scrambled on claustrum
	// (chunks 2, 3, 0, 8, 4 …), because each request ran in its own goroutine and
	// raced to the queue.
	stdinMu     sync.Mutex
	stdinCond   *sync.Cond
	stdinNext   uint64 // next ticket to hand out
	stdinServed uint64 // ticket currently admitted

	// ownEnv holds the entries of the last good server.adoptEnv of this connection
	// that reach only its own children (see supervisor.Manager.TakeAdoptedEnv).
	ownEnvMu sync.Mutex
	ownEnv   []string
	// readsLoginPATH is true when the last good server.adoptEnv of this connection
	// came with no "noLoginShell":true.
	readsLoginPATH bool
}

// SetOwnEnv replaces the own entries of the adopted environment of c.
func (c *Conn) SetOwnEnv(env []string) {
	c.ownEnvMu.Lock()
	defer c.ownEnvMu.Unlock()
	c.ownEnv = env
}

// OwnEnv is a copy of the own entries of the adopted environment of c. A nil conn
// has none.
func (c *Conn) OwnEnv() []string {
	if c == nil {
		return nil
	}
	c.ownEnvMu.Lock()
	defer c.ownEnvMu.Unlock()
	return append([]string(nil), c.ownEnv...)
}

// SetReadsLoginPATH records whether the last good server.adoptEnv of c lets its
// spawns can start a read again of the login-shell PATH.
func (c *Conn) SetReadsLoginPATH(on bool) {
	c.ownEnvMu.Lock()
	defer c.ownEnvMu.Unlock()
	c.readsLoginPATH = on
}

// ReadsLoginPATH tells if the spawns of c can start a read again of the login-shell
// PATH. A nil conn and a conn with no good adopt do not.
func (c *Conn) ReadsLoginPATH() bool {
	if c == nil {
		return false
	}
	c.ownEnvMu.Lock()
	defer c.ownEnvMu.Unlock()
	return c.readsLoginPATH
}

// New builds a Conn. The accept loop passes every value. A test passes only
// what it needs, and the zero value of each other one.
func New(nc net.Conn, ac *ActivityConn, id uint64, wlog *wirelog.Log, done chan struct{}, down <-chan struct{}) *Conn {
	return &Conn{nc: nc, ac: ac, id: id, wlog: wlog, done: done, down: down}
}

// NC is the net.Conn of c.
func (c *Conn) NC() net.Conn { return c.nc }

// AC is the activity wrapper of c, nil for a conn that a test builds around a bare pipe.
func (c *Conn) AC() *ActivityConn { return c.ac }

// Done is the channel that the read loop closes when it returns.
func (c *Conn) Done() chan struct{} { return c.done }

// ID is the -wire-log id of c.
func (c *Conn) ID() uint64 { return c.id }

// Down is the shutdown channel of the daemon.
func (c *Conn) Down() <-chan struct{} { return c.down }

// WireLog is the -wire-log recorder of c, nil when off.
func (c *Conn) WireLog() *wirelog.Log { return c.wlog }

// SentStop reports that c sent server.shutdown. The caller holds the lock of the server.
func (c *Conn) SentStop() bool { return c.sentStop }

// SetSentStop marks that c sent server.shutdown. The caller holds the lock of the server.
func (c *Conn) SetSentStop() { c.sentStop = true }

// BareShown is the token mark of a conn that has no activity wrapper.
func (c *Conn) BareShown() *atomic.Bool { return &c.bareShown }

// MarkClosed marks the writer of c closed under its write lock.
func (c *Conn) MarkClosed() {
	c.wmu.Lock()
	c.closed = true
	c.wmu.Unlock()
}

// MarkClosedIfFree marks the writer of c closed only if no writer holds its
// write lock. It reports whether it did.
func (c *Conn) MarkClosedIfFree() bool {
	if !c.wmu.TryLock() {
		return false
	}
	c.closed = true
	c.wmu.Unlock()
	return true
}

// Supersede ends this connection because another one took over what it was
// attached to, matching reference build 90fca6e6.
//
// Before 90fca6e6 a reattach only transferred the frame stream: the previously
// attached connection stopped receiving frames but stayed open, so a client that
// had lost the session could not tell its old connection was finished. Measured:
// on 19f30c46 the old connection still answers server.ping after another
// connection reattaches, and on 90fca6e6 the next write to it fails.
//
// The claim makes it happen once. A connection the idle watcher already ended
// is not closed and logged a second time, and neither is one superseded twice.
//
// The reason text is claustrum's own phrasing, not the reference's verbatim
// string, the same choice the host cleaner made for its log reasons. What is
// reproduced is the close, which a client observes; the wording goes to stderr.
func (c *Conn) Supersede(reason string) {
	// The accept loop is the only place production builds a conn and it always
	// sets ac, on every transport and every OS. A nil ac therefore means a
	// test-built conn around a bare pipe, and there is no flag to claim.
	//
	// The warning is deliberate. A nil ac silently disables this whole path, and
	// every supersede assertion then passes while nothing is superseded. That is
	// not hypothetical: the socket harness handed serveConn a bare net.Conn until
	// this change, so a future regression of the same shape says so out loud.
	if c.ac == nil {
		logging.Warnf("[Server] supersede on a connection with no activity wrapper; not closing it")
		return
	}
	// Mark the writer closed whether or not we win the claim. Losing it means the
	// idle watcher is closing this connection instead, so a later WriteLine that
	// slipped past the closed check would record a frame into a -wire-log capture
	// that never reaches the wire. (The idle watcher still cannot set this itself:
	// it holds the ActivityConn, not the conn.)
	c.wmu.Lock()
	c.closed = true
	c.wmu.Unlock()
	if !c.ac.ClaimClose() {
		return
	}
	logging.Infof("[Server] closing connection %s: %s", c.RemoteLabel(), reason)
	_ = c.nc.Close()
}

// NextStdinTicket reserves this request's place in the stdin stream. Called from
// the connection's read loop, so tickets follow wire order exactly.
func (c *Conn) NextStdinTicket() uint64 {
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()
	if c.stdinCond == nil {
		c.stdinCond = sync.NewCond(&c.stdinMu)
	}
	t := c.stdinNext
	c.stdinNext++
	return t
}

// AwaitStdinTurn blocks until every earlier stdin request on this connection has
// finished.
func (c *Conn) AwaitStdinTurn(t uint64) {
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()
	for c.stdinServed != t {
		c.stdinCond.Wait()
	}
}

// DoneStdinTurn admits the next stdin request.
func (c *Conn) DoneStdinTurn(t uint64) {
	c.stdinMu.Lock()
	c.stdinServed = t + 1
	c.stdinCond.Broadcast()
	c.stdinMu.Unlock()
}

// WriteJSON is one of several outbound encodes — see also Conn.WriteResponse
// below (every JSON-RPC reply), supervisor.Proc.emit (every live stream frame),
// writeFramesLocked in internal/supervisor/process.go (the replay of a reattach
// that holds the write lock) and peerRefusalLine in peercheck.go (the refusal of a
// peer). This one carries reattach REPLAY frames only; the comment used to call it
// "the single outbound encode", which would send someone doing an encoder cleanup
// looking in exactly one place and missing the others.
//
// All of them must stay on json.Marshal, not an Encoder: Marshal HTML-escapes
// `< > &` with no opt-out, which is what the reference emits, and
// SetEscapeHTML(false) would look like a cleanup and move the wire — see
// docs/ARCHITECTURE.md → "Inherited wire bytes".
func (c *Conn) WriteJSON(v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.WriteLine(append(b, '\n'))
}

// WriteLine writes an already-marshaled, newline-terminated JSON line under the
// same lock/closed semantics as WriteJSON. It lets a frame fanned out to many
// subscribers be marshaled once (see supervisor.Proc.emit) instead of once per conn.
// Write does not mutate b, so the same slice is safe to hand to every conn.
func (c *Conn) WriteLine(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.WriteLineLocked(b)
}

// LockWrites takes the write lock of c. Process code holds it across a
// reattach and then writes with writeFramesLocked.
func (c *Conn) LockWrites() { c.wmu.Lock() }

// UnlockWrites releases the write lock that LockWrites took.
func (c *Conn) UnlockWrites() { c.wmu.Unlock() }

// ClosedNow reports whether c is closed. It takes the write lock of c.
func (c *Conn) ClosedNow() bool {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.closed
}

// WriteLineLocked is WriteLine with c.wmu already held.
func (c *Conn) WriteLineLocked(b []byte) error {
	if c.closed {
		return net.ErrClosed
	}
	// Recorded here — under wmu, past the closed check, just before the write — so
	// the capture holds only frames that actually go on the wire, in wire order.
	// Recording before the lock would let two writers log in one order and write
	// in another, and would log a frame the closed check then drops. A frame
	// fanned out to N subscribers is recorded once per conn: a per-connection view.
	b = StampEnd(b, c.sent)
	c.wlog.Record(c.id, "out", b)
	n, err := c.nc.Write(b)
	c.sent += int64(n)
	return err
}

// WriteResponse writes a JSON-RPC response and, on a write error (e.g. the
// client dropped the connection mid-reply), emits the reference daemon's
// writeResponse/Failed-to-write log lines with the partial byte count. It
// reports a failed write. Only the read loop acts on that, for its -32001 and
// -32700 replies.
func (c *Conn) WriteResponse(v interface{}) (failed bool) {
	// json.Marshal, not an Encoder: this is the encode that carries EVERY
	// JSON-RPC reply, and its HTML escaping is inherited wire behaviour — see
	// docs/ARCHITECTURE.md → "Inherited wire bytes" and the note on WriteJSON.
	b, err := json.Marshal(v)
	if err != nil {
		if !c.ShuttingDown() {
			logging.Errorf(FmtReplyFailed, err)
		}
		return false
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return false
	}
	// Recorded under wmu, past the closed check, just before the write, so the
	// capture is exactly what goes on the wire — in wire order, and never a frame
	// the closed check dropped. Record neither retains nor mutates b.
	b = StampEnd(b, c.sent)
	c.wlog.Record(c.id, "out", b)
	n, err := c.nc.Write(b)
	c.sent += int64(n)
	if err != nil {
		// Claustrum's own, not measured: the error text is quoted, the first line
		// is skipped for a connection that never showed the token, and both lines
		// are skipped once the daemon shuts down.
		if !c.ShuttingDown() {
			if c.TokenMark().Load() {
				logging.Debugf(FmtWriteFailed, n, len(b), err)
			}
			logging.Errorf(FmtReplyFailed, err)
		}
		return true
	}
	return false
}
