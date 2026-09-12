//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// machineIDPaths are the files machineID reads, in order (reference build 19f30c46:
// /etc/machine-id, then /var/lib/dbus/machine-id). A var so a test can point it at
// temp files and exercise the fallback without depending on the host's real id.
var machineIDPaths = []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}

// recordChild writes the orphan-registry record for a just-spawned child under a
// run-shaped socket (reference build 19f30c46). Best-effort and non-destructive: a
// failure is logged, never fatal. It is a no-op when the socket is not
// run/<clientId>/ shaped (no runDir), when pid < 2, or when the child's start-time
// cannot be read — the reference skips the record in exactly those cases, since
// without a pid-reuse-safe start-time the record could not identify the child later.
// The record's identity (node, host, instance, the daemon's pid and start-time) is
// what a later daemon uses to decide whether a leftover child of a since-exited
// daemon is an orphan to reap. Reaping itself is a separate slice.
func (m *procManager) recordChild(pid int, argv0 string) {
	if m.runDir == "" || pid < 2 {
		return
	}
	start := procStartTicks(pid)
	if start == 0 {
		return
	}
	err := writeChildRecord(m.runDir, childRecord{
		Pid:         pid,
		Node:        nodeID(),
		Host:        "machine-id:" + machineID(),
		Instance:    m.instanceID,
		DaemonPid:   os.Getpid(),
		DaemonStart: strconv.FormatInt(ownStartTicks(), 10),
		Argv0:       argv0,
		Start:       strconv.FormatInt(start, 10),
		At:          time.Now().UnixMilli(),
	})
	if err != nil {
		logErrorf("[process.Manager] failed to record child %d: %v", pid, err)
	}
}

// machineID returns the host machine id (reference build 19f30c46: /etc/machine-id,
// falling back to /var/lib/dbus/machine-id), trimmed, or "" when neither is readable.
func machineID() string {
	for _, p := range machineIDPaths {
		if b, err := os.ReadFile(p); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id
			}
		}
	}
	return ""
}

// procStartTicks returns pid's start-time in clock ticks since boot — field 22 of
// /proc/<pid>/stat, the pid-reuse-safe value the child's CLAUDE_SSH_CHILD marker also
// carries. It is the generalization of ownStartTicks (which reads /proc/self/stat) and
// shares its field-22 parser. Returns 0 when the process is gone or stat cannot be
// parsed.
func procStartTicks(pid int) int64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	return parseStartTicks(string(b))
}
