package main

import (
	"bufio"
	"encoding/json"
	"net"
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
// instanceId) fail to reach THIS daemon. Any connected client, or a probe that reaches
// us, resets it. It is cross-platform: os.SameFile compares the socket's file identity
// portably (device+inode on unix, a file-handle compare on Windows), so a rebound path
// is detected the same way on every OS.
//
// Shutdown is graceful (signalShutdown drives the same teardown as server.shutdown:
// listeners closed, clients dropped, and child process groups stopped unless
// -keep-children), never a bare exit.

// Orphan-exit timing. Package vars so a test can shrink them; the reference uses a 60s
// check interval and a 600s (10 min) grace.
var (
	orphanCheckInterval = 60 * time.Second
	orphanGrace         = 10 * time.Minute
)

// orphanProbeTimeout bounds the self-probe's dial and read.
const orphanProbeTimeout = 2 * time.Second

// orphanProbeFailuresToExit is the number of consecutive failed self-probes required
// before an orphaned daemon shuts down: the first logs and re-checks, the second acts.
const orphanProbeFailuresToExit = 2

// exitWhenOrphaned runs as a goroutine for the daemon's life. It is a no-op (returns at
// once) unless both durations are positive, the bound socket's identity was captured
// (sockInfo), and this daemon has an instanceId to recognise itself by.
func (s *server) exitWhenOrphaned(socket string) {
	if orphanCheckInterval <= 0 || orphanGrace <= 0 || s.sockInfo == nil || s.instanceID == "" {
		return
	}
	t := time.NewTicker(orphanCheckInterval)
	defer t.Stop()

	var orphanedSince time.Time
	failedProbes := 0
	reset := func() {
		orphanedSince = time.Time{}
		failedProbes = 0
	}

	for {
		select {
		case <-s.shutdown:
			return
		case <-t.C:
		}

		// A connected client always resets: never exit while someone is attached, and
		// require the two failed probes to be consecutive with nobody connecting between.
		if s.connCount() > 0 {
			reset()
			continue
		}
		if !s.socketLooksOrphaned(socket) {
			reset()
			continue
		}
		if orphanedSince.IsZero() {
			orphanedSince = time.Now()
			logInfof("[Server] socket %s no longer resolves to this daemon; will shut down if that holds for %s with no client connected", socket, orphanGrace)
			continue
		}
		if time.Since(orphanedSince) < orphanGrace {
			continue
		}
		// Grace elapsed with no client connected: probe whether the path still reaches us.
		if s.pathReachesUs(socket) {
			logInfof("[Server] socket %s still reaches this daemon despite a changed file identity; not orphaned", socket)
			reset()
			continue
		}
		failedProbes++
		if failedProbes < orphanProbeFailuresToExit {
			logWarnf("[Server] socket %s did not lead back to this daemon (%d/%d); re-checking next interval", socket, failedProbes, orphanProbeFailuresToExit)
			continue
		}
		logWarnf("[Server] orphaned for %s (socket gone or rebound, %d self-probes failed, no clients); shutting down with %d child process group(s) running",
			time.Since(orphanedSince).Round(time.Second), failedProbes, s.procs.runningCount())
		s.signalShutdown()
		return
	}
}

// connCount returns the number of currently connected clients.
func (s *server) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// socketLooksOrphaned reports whether the socket path no longer resolves to the inode
// this daemon bound: the file is gone, or it is a different file (a successor rebound the
// path). os.SameFile compares file identity portably (device+inode on unix, a file handle
// on Windows). A stat error other than "not exist" is treated as inconclusive (not
// orphaned) so a transient error never triggers a shutdown.
func (s *server) socketLooksOrphaned(socket string) bool {
	cur, err := os.Stat(socket)
	if err != nil {
		return os.IsNotExist(err)
	}
	return !os.SameFile(cur, s.sockInfo)
}

// pathReachesUs dials the socket path and asks server.capabilities, returning true only
// when the reply's instanceId is our own — the path still leads back to THIS daemon. Any
// dial, write, read, or parse failure, or a different instanceId, returns false.
func (s *server) pathReachesUs(socket string) bool {
	c, err := net.DialTimeout("unix", socket, orphanProbeTimeout)
	if err != nil {
		return false
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
		return false
	}
	if _, err := c.Write(append(line, '\n')); err != nil {
		return false
	}
	reply, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil && len(reply) == 0 {
		return false
	}
	var parsed struct {
		Result struct {
			InstanceID string `json:"instanceId"`
		} `json:"result"`
	}
	if json.Unmarshal(reply, &parsed) != nil {
		return false
	}
	return parsed.Result.InstanceID != "" && parsed.Result.InstanceID == s.instanceID
}
