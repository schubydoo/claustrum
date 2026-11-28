package main

import (
	"os"
	"sync/atomic"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/leftsweep"
	"github.com/schubydoo/claustrum/internal/logging"
)

// The end of the daemon at the unattended limit of the host settings file (13047a51,
// measured on Linux). A daemon with a limit from its start read, and with no connection
// for that long, logs its line and "[Server] shutdown requested". Then it stops by the
// same teardown as server.shutdown: the socket and daemon.token go, and the group of each
// plain child gets its SIGKILL (closeAll). Then the left-process sweep runs
// (sweepAfterLimit).
//
// Measured on Linux with a limit of 1 that counts as 15: the check runs at each tick of
// the 60 s ticker from the start of the daemon (orphanCheckInterval), and the first tick
// with 15 minutes alone ends it. The clock also counts from the start. Only the close of a
// connection that sent a request with the token starts it again. A wrong token or a dial
// with no byte does not. A connected client holds the end off for as long as it stays.
// Live children got a group SIGKILL, and the line counted them. A child with stream-json
// in its args gets the SIGTERM of the shutdown sweep first. 13047a51 sends the group
// SIGKILL twice for a ccd-cli or launcher spawn, and claustrum sends it once (claustrum's
// own).
//
// Claustrum's own, not measured: a dial of this daemon to itself does not start the clock
// again. The check runs before the idle exit and the orphan checks, and not once the
// shutdown began. With -keep-children the children stay.

// limitEnd ends the daemon at the check at the time now (monoNow) when the limit is due.
// clients is the number of connected clients at that check. It returns true once it
// started the shutdown.
func (s *server) limitEnd(socket string, clients int, now time.Duration) bool {
	if !limitEndRuns || s.hostLimit.minutes <= 0 || clients > 0 || s.shutdownBegun() {
		return false
	}
	alone := now - time.Duration(s.lastAttended.Load())
	if alone < time.Duration(s.hostLimit.minutes)*time.Minute {
		return false
	}
	// The lines and the mark come only from the call that starts the shutdown, before its
	// start, so closeAll sees the mark. A shutdown that another path started gets no limit
	// line and runs no sweep.
	started := false
	s.once.Do(func() {
		started = true
		logging.Warnf("[Server] nobody has used this helper or been connected to it for %s, and the unattended limit in force is %d minutes (%s): shutting down and killing %d child process(es)",
			alone.Truncate(time.Second), s.hostLimit.minutes, s.hostLimit.path, s.procs.AliveCount())
		if !s.socketLeadsHere(socket) {
			logging.Warnf("[Server] socket path %s can't be shown to lead to this helper any more, so only its children's own process groups are ended: what has left one may still be running", socket)
			s.limitNoSweep.Store(true)
		}
		logging.Infof("[Server] shutdown requested")
		s.limitStopped.Store(true)
		s.beginShutdown()
	})
	return started
}

// socketLeadsHere is true while the socket path names the socket that this daemon bound.
// 13047a51 logs the line of limitEnd and runs no sweep for a socket path that is gone,
// a regular file, or a link to the socket of another daemon (measured on Linux). A stat
// error counts as such a path, and no self-probe runs (claustrum's own, not measured).
func (s *server) socketLeadsHere(socket string) bool {
	cur, err := os.Stat(socket)
	return err == nil && s.sockInfo != nil && os.SameFile(cur, s.sockInfo)
}

// shutdownBegun is true once the shutdown started.
func (s *server) shutdownBegun() bool {
	select {
	case <-s.shutdown:
		return true
	default:
		return false
	}
}

// limitSelfPID is the pid of this daemon for the self-dial test of the limit clock. A
// test sets it, so that its own dials count as those of a client. It is atomic, because
// a connection of an earlier test can still read it.
var limitSelfPID atomic.Int64

func init() { limitSelfPID.Store(int64(os.Getpid())) }

// dialedBySelf reports whether the peer of c is this daemon itself, such as the
// self-probe of the orphan checks. The close of such a connection does not start the
// limit clock again (claustrum's own, not measured). The peer is read by SO_PEERCRED,
// on Linux only.
func (s *server) dialedBySelf(c *conn.Conn) bool {
	if !limitEndRuns {
		return false
	}
	nc := c.NC()
	if ac := c.AC(); ac != nil {
		nc = ac.Conn
	}
	pid := connPeerPID(nc)
	return pid != 0 && int64(pid) == limitSelfPID.Load()
}

// noteAttended restarts the clock of the limit end. newServerOnSocket calls it once,
// and serveConn calls it at the close of each connection that showed the token and
// that this daemon did not dial itself.
func (s *server) noteAttended() { s.lastAttended.Store(int64(monoNow())) }

// sweepAfterLimit ends the processes that the children started and left, after an end
// at the limit. closeAll calls it right after the group kills. 13047a51 logs its line
// after the exit lines of the children and before the cleanup line, and logs none after
// server.shutdown or the orphan end (measured on Linux). With -keep-children no sweep
// runs, because the children stay (claustrum's own, not measured). Nor does one run for a
// socket path that no longer leads here (socketLeadsHere).
func (s *server) sweepAfterLimit() {
	if !s.limitStopped.Load() || s.limitNoSweep.Load() || s.keepChildren || s.procs.RunDir == "" {
		return
	}
	n, err := leftsweep.Run(s.procs.RunDir)
	if err != nil {
		// The line of a failed sweep is claustrum's own, not measured.
		logging.Warnf("[Server] cleanup: ended %d more process(es) that the children had started and left, then the search stopped: %v", n, err)
		return
	}
	logging.Infof("[Server] cleanup: ended %d more process(es) that the children had started and left", n)
}
