//go:build darwin

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// recordChild writes the orphan-registry record for a just-spawned child under a
// run-shaped socket (reference build 19f30c46, darwin). Best-effort and
// non-destructive: a failure is logged, never fatal. It is a no-op when the socket is
// not run/<clientId>/ shaped (no runDir), when pid < 2, or when the child's
// start-time cannot be read — matching the reference, which skips the record without a
// pid-reuse-safe start-time. The record's identity differs from linux because darwin
// has no /proc: the node is the boot-session UUID, the host is the machine hostname,
// and the start-times are the process start formatted as a UTC ANSIC timestamp (the
// same value `ps -o lstart` prints under TZ=UTC) rather than /proc clock ticks. The
// on-disk record (field order, string-vs-number typing, path) is identical to linux.
func (m *procManager) recordChild(pid int, argv0 string) {
	if m.runDir == "" || pid < 2 {
		return
	}
	start := darwinProcStart(pid)
	if start == "" {
		return
	}
	err := writeChildRecord(m.runDir, childRecord{
		Pid:         pid,
		Node:        bootSessionUUID(),
		Host:        darwinHostname(),
		Instance:    m.instanceID,
		DaemonPid:   os.Getpid(),
		DaemonStart: darwinProcStart(os.Getpid()),
		Argv0:       argv0,
		Start:       start,
		At:          time.Now().UnixMilli(),
	})
	if err != nil {
		logErrorf("[process.Manager] failed to record child %d: %v", pid, err)
	}
}

// bootSessionUUID returns this boot's session UUID (reference build 19f30c46 darwin:
// sysctl kern.bootsessionuuid), the darwin analogue of linux's boot_id. It is the
// record's node value. Returns "" when the sysctl is unreadable. A seam so a test can
// supply a fixed id without depending on the host.
var bootSessionUUID = func() string {
	id, err := syscall.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return ""
	}
	return id
}

// darwinHostname returns the machine hostname — the record's host value on darwin
// (the reference stores the bare hostname, not linux's "machine-id:<id>"). A seam for
// tests. Returns "" when the hostname is unreadable.
var darwinHostname = func() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// darwinProcStart returns pid's start-time as a UTC ANSIC timestamp string (e.g.
// "Sun Sep 13 07:56:41 2026"), the pid-reuse-safe value the record and the
// CLAUDE_SSH_CHILD marker carry on darwin. It matches `ps -o lstart` under TZ=UTC —
// which is exactly how it is read: darwin has no /proc/<pid>/stat, so the start-time
// comes from ps with a pinned, UTC environment so the format is stable and matches the
// reader used at reap time. Returns "" when the process is gone or ps fails. A seam so
// tests never shell out. It is the darwin analogue of procStartTicks.
var darwinProcStart = func(pid int) string {
	cmd := exec.Command("ps", "-ww", "-o", "lstart=", "-p", strconv.Itoa(pid))
	// TZ=UTC forces the UTC rendering the reference records; C locale + a fixed PATH
	// keep the field format stable and independent of the caller's environment.
	cmd.Env = []string{"TZ=UTC", "LC_ALL=C", "LANG=C", "PATH=/bin:/usr/bin:/usr/sbin"}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
