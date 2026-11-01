//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// machineIDPaths are the files machineID reads, in order (/etc/machine-id, then
// /var/lib/dbus/machine-id). A var so a test can point it at
// temp files and exercise the fallback without depending on the host's real id.
var machineIDPaths = []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}

// recordChild writes the orphan-registry record for a just-spawned child (reference
// build 19f30c46), on every socket shape (89cb6289, Linux rows F1 to F4).
// Best-effort and non-destructive: a
// failure is logged, never fatal. It is a no-op when the manager has no run dir
// (no socket), when pid < 2, or when the child's start-time
// cannot be read. Without a pid-reuse-safe start-time, the record cannot identify the
// child later. With no usable /proc it logs one line and makes no children folder
// (be1c1ca5, measured on Linux). A start time that cannot be read while /proc works
// logs nothing, as before: that case is not measured.
// The record's identity (node, host, instance, the daemon's pid and start-time) is
// what a later daemon uses to decide whether a leftover child of a since-exited
// daemon is an orphan to reap. Reaping itself is a separate slice.
func (m *procManager) recordChild(pid int, argv0, program string) {
	if m.runDir == "" || pid < 2 || m.noRecords {
		return
	}
	start := procStartTicks(pid)
	if start == 0 {
		if !procUsable() {
			logWarnf("[process.Manager] cannot read the start time of pid %d (no /proc and no ps on this platform): a successor daemon will not be able to reap it", pid)
		}
		return
	}
	m.writeRecord(childRecord{
		Pid:         pid,
		Node:        nodeID(),
		Host:        "machine-id:" + machineID(),
		Instance:    m.instanceID,
		DaemonPid:   os.Getpid(),
		DaemonStart: strconv.FormatInt(ownStartTicks(), 10),
		Argv0:       argv0,
		Program:     program,
		Start:       strconv.FormatInt(start, 10),
		At:          time.Now().UnixMilli(),
	})
}

// machineID returns the host machine id (/etc/machine-id, falling back to
// /var/lib/dbus/machine-id), trimmed, or "" when neither is readable.
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
	b, err := os.ReadFile(procRoot + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	return parseStartTicks(string(b))
}

// procUsable reports whether procRoot answers for this process. A /proc with no
// self/stat counts as no /proc (from the code).
func procUsable() bool {
	_, err := os.Stat(procRoot + "/self/stat")
	return err == nil
}
