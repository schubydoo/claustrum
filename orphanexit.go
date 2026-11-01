package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"time"
)

// Orphan-exit self-probe, matching reference build 4534d86. A -serve daemon whose socket
// path has been taken over by a newer daemon (the file was unlinked, or rebound to a
// different inode) is "orphaned": clients now reach the successor, not it. An orphaned
// daemon with no clients connected shuts itself down, so a restart's predecessor retires
// promptly once the successor owns the socket. Without this it would linger indefinitely
// — the idle timeout closes only an idle connection, never the daemon.
//
// The check is deliberately conservative. It acts only after the socket has looked
// orphaned continuously for orphanGrace with zero clients the whole time, AND two
// consecutive self-probes (dial the socket, ask server.capabilities, compare the
// instanceId) fail to reach THIS daemon. A path that still matches, or a probe that
// reaches us, resets it. A check that sees a client starts the grace again with no
// line (selfExitCheck). os.SameFile compares the socket's file identity (device+inode).
// The checks run on Linux and macOS only (selfExitRuns).
//
// The same checks also run the idle exit: no counted request for idleExitAfter and no
// client at a check shuts the daemon down.
//
// Shutdown is graceful (signalShutdown drives the same teardown as server.shutdown:
// listeners closed, clients dropped, and child process groups stopped unless
// -keep-children), never a bare exit.

// Orphan-exit timing. Package vars so a test can shrink them.
//
// Measured on a Linux VM (rows ST09 and ST11, several runs). The reference prints the
// "no longer leads to this daemon" line with "10m0s", then the "(1/2)" line. Then it
// prints the "orphaned for" line and the two shutdown lines. In one run 89cb6289 printed
// "(1/2)" 9 min 59 s after the first line and "orphaned for 11m0s" one minute later.
// 89cb6289 printed "11m0s" in four rows and "12m0s" in three. f6010b97 printed "12m0s" in
// its one row. The texts of these lines are the reference's. claustrum printed the same
// texts in those rows, with "12m0s" in each of six rows. The cause of the 60 s
// difference is not measured. The check interval of the reference is not measured.
var (
	orphanCheckInterval = 60 * time.Second
	orphanGrace         = 10 * time.Minute
)

// orphanProbeTimeout bounds the self-probe's dial and read.
const orphanProbeTimeout = 2 * time.Second

// orphanProbeFailuresToExit is the number of consecutive failed self-probes required
// before an orphaned daemon shuts down: the first logs and re-checks, the second acts.
const orphanProbeFailuresToExit = 2

// idleExitAfter is the idle limit. A daemon that had no counted request for this long,
// and has no client at a check, shuts down (see noteCountedRequest). It is a package
// var so a test can set a short limit. No flag, conf key or environment variable sets
// it. The limit and the counted kinds are not measured.
var idleExitAfter = 30 * 24 * time.Hour

// monoOrigin is the zero of the request clock. time.Since on it reads the monotonic
// clock, so a step of the wall clock does not move the idle time.
var monoOrigin = time.Now()

// monoNow is the time since monoOrigin on the monotonic clock.
func monoNow() time.Duration { return time.Since(monoOrigin) }

// noteCountedRequest restarts the idle time. gate calls it for each request that
// passed the auth step, except server.capabilities: the self-probe sends that method,
// so the probe never restarts the idle time. newServerOnSocket calls it once, so the
// idle time also counts from the start of the daemon.
func (s *server) noteCountedRequest() { s.lastCounted.Store(int64(monoNow())) }

// selfExitWatch is the orphan state that one check hands to the next.
type selfExitWatch struct {
	orphaned     bool
	orphanedAt   time.Duration // monoNow at the start of the grace
	failedProbes int
}

// exitWhenOrphaned runs as a goroutine for the daemon's life. It is a no-op (returns at
// once) on Windows (selfExitRuns), and unless both durations are positive, the bound
// socket's identity was captured (sockInfo), and this daemon has an instanceId to
// recognise itself by.
func (s *server) exitWhenOrphaned(socket string) {
	if !selfExitRuns || orphanCheckInterval <= 0 || orphanGrace <= 0 || s.sockInfo == nil || s.instanceID == "" {
		return
	}
	t := time.NewTicker(orphanCheckInterval)
	defer t.Stop()

	var w selfExitWatch
	for {
		select {
		case <-s.shutdown:
			return
		case <-t.C:
		}
		if s.selfExitCheck(socket, &w, monoNow()) {
			return
		}
	}
}

// selfExitCheck runs one check at the time now (monoNow). It returns true once it
// started the shutdown.
func (s *server) selfExitCheck(socket string, w *selfExitWatch, now time.Duration) bool {
	clients := s.connCount()
	// The idle exit comes first, so a check where both exits are due writes the idle
	// line and not the orphan lines. That order is not measured.
	if idle := now - time.Duration(s.lastCounted.Load()); clients == 0 && idle >= idleExitAfter {
		logInfof("[Server] no request for %s and no connections; shutting down", idle.Round(time.Second))
		logInfof("[Server] shutdown requested")
		s.signalShutdown()
		return true
	}
	if !s.socketLooksOrphaned(socket) {
		*w = selfExitWatch{}
		return false
	}
	// Orphaned while a client is connected: no line, and the grace and the two probes
	// count again from this check. So the "no longer leads" line does not come after
	// that client leaves (cell K2 of round tier1-01, Linux and macOS VMs).
	if clients > 0 {
		*w = selfExitWatch{orphaned: true, orphanedAt: now}
		return false
	}
	if !w.orphaned {
		*w = selfExitWatch{orphaned: true, orphanedAt: now}
		logInfof("[Server] socket path %s no longer leads to this daemon; shutting down if that holds for %s with nobody connected", socket, orphanGrace)
		return false
	}
	if now-w.orphanedAt < orphanGrace {
		return false
	}
	// Grace elapsed with no client connected: probe whether the path still reaches us.
	reached, cannotDial := s.pathReachesUs(socket)
	if reached {
		logInfof("[Server] socket %s still reaches this daemon despite a changed file identity; not orphaned", socket)
		*w = selfExitWatch{}
		return false
	}
	// A path that this daemon cannot dial without a change of folder: a file with the
	// mtime of the bound socket is not orphaned. The text and the outcome are those of
	// `be1c1ca5` on macOS, for a plain file with that mtime (measured). Another mtime
	// counts as a failed probe there (measured on macOS). A gone file counts as one too,
	// and the grace starts again after this line (claustrum's own, not measured).
	if cannotDial && s.socketFileUnmodified(socket) {
		logInfof("[Server] socket path %s is too long for this daemon to dial, but its file is unmodified since this daemon bound it; not orphaned", socket)
		*w = selfExitWatch{}
		return false
	}
	w.failedProbes++
	if w.failedProbes < orphanProbeFailuresToExit {
		logWarnf("[Server] socket path %s did not lead back to this daemon (%d/%d); re-checking before acting", socket, w.failedProbes, orphanProbeFailuresToExit)
		return false
	}
	logWarnf("[Server] orphaned for %s (socket path gone or re-bound, %d self-probes failed, no connections); shutting down and killing %d child process(es)",
		(now - w.orphanedAt).Round(time.Second), w.failedProbes, s.procs.runningCount())
	logInfof("[Server] shutdown requested")
	s.signalShutdown()
	return true
}

// connCount returns the number of currently connected clients.
func (s *server) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// socketLooksOrphaned reports whether the socket path no longer resolves to the inode
// this daemon bound: the file is gone, or it is a different file (a successor rebound the
// path). os.SameFile compares the device and inode. A stat error other than "not exist"
// is treated as inconclusive (not orphaned) so a transient error never triggers a
// shutdown.
func (s *server) socketLooksOrphaned(socket string) bool {
	cur, err := os.Stat(socket)
	if err != nil {
		return os.IsNotExist(err)
	}
	return !os.SameFile(cur, s.sockInfo)
}

// socketFileUnmodified reports whether the file at the socket path has the mtime of
// the socket that this daemon bound.
func (s *server) socketFileUnmodified(socket string) bool {
	cur, err := os.Stat(socket)
	return err == nil && cur.ModTime().Equal(s.sockInfo.ModTime())
}

// pathReachesUs dials the socket path and asks server.capabilities. reached is true only
// when the reply's instanceId is our own — the path still leads back to THIS daemon. Any
// dial, write, read, or parse failure, or a different instanceId, gives false.
// cannotDial is true when the path is too long to dial without a change of folder
// (dialSocketInDaemon). On Linux `be1c1ca5` ends the daemon at a long path when a
// plain file with the bound mtime is there (measured on Linux). This probe dials
// through /proc there.
func (s *server) pathReachesUs(socket string) (reached, cannotDial bool) {
	c, err := dialSocketInDaemon(socket, orphanProbeTimeout)
	if err != nil {
		var tooLong *socketTooLongError
		return false, errors.As(err, &tooLong)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(orphanProbeTimeout))

	line, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "server.capabilities",
		"auth":    s.token,
	})
	if err != nil {
		return false, false
	}
	if _, err := c.Write(append(line, '\n')); err != nil {
		return false, false
	}
	reply, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil && len(reply) == 0 {
		return false, false
	}
	var parsed struct {
		Result struct {
			InstanceID string `json:"instanceId"`
		} `json:"result"`
	}
	if json.Unmarshal(reply, &parsed) != nil {
		return false, false
	}
	return parsed.Result.InstanceID != "" && parsed.Result.InstanceID == s.instanceID, false
}
