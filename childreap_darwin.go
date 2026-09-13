//go:build darwin

package main

import (
	"encoding/binary"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// The darwin-specific half of the orphan reap: it reads a live process's group id, state and
// program via `ps` (darwin has no /proc), its start-time via darwinProcStart, and its
// environment markers via KERN_PROCARGS2. It also gathers this daemon's node/host identity
// from the darwin sources. The shared decision logic (reapOrphans, verifyOrphan, the two-phase
// wait) lives in childreap.go.

// ownReapIdentity returns this daemon's node and host for the reap's record-matching gate,
// from the same darwin sources childrecord writes: node = the boot-session UUID, host =
// the machine hostname.
func ownReapIdentity() (node, host string) {
	return bootSessionUUID(), darwinHostname()
}

// psReapEnv is the pinned environment for the reap's `ps` invocations. TZ=UTC forces the
// same UTC start-time rendering childrecord recorded, so the pid-reuse start-time compares
// equal; C locale + a fixed PATH keep the field format stable. A seam so a test can drive
// the reader without a real ps.
var psReapEnv = []string{"TZ=UTC", "LC_ALL=C", "LANG=C", "PATH=/bin:/usr/bin:/usr/sbin"}

// runPS runs `ps` with the pinned env and the given -o keyword string for one pid, and
// returns the single trimmed output line (empty when ps found no such process). A seam for
// tests.
var runPS = func(pid int, keys string) string {
	cmd := exec.Command("ps", "-ww", "-o", keys, "-p", strconv.Itoa(pid))
	cmd.Env = psReapEnv
	out, err := cmd.Output()
	if err != nil {
		return "" // non-zero exit (no such process) or spawn error: treat as gone
	}
	return strings.TrimRight(string(out), "\n")
}

// realReadLiveProc reads a live process for the reap. It always reads the group id, state and
// program via `ps`; the start-time comes from darwinProcStart so it is byte-identical to the
// value childrecord recorded (the pid-reuse guard compares them as strings). When wantEnv is
// set it also reads the two env markers from the process's environment.
func realReadLiveProc(pid int, wantEnv bool) liveProc {
	lp := liveProc{state: procGone}
	start := darwinProcStart(pid)
	if start == "" {
		return lp // gone (ps -o lstart found nothing)
	}
	line := runPS(pid, "pgid=,stat=,command=")
	if line == "" {
		return lp // gone
	}
	fields := strings.Fields(line)
	if len(fields) < 3 {
		lp.state = procNotOurs
		return lp
	}
	// A zombie (state begins with 'Z') is gone; there is no separate dead state in ps output.
	if strings.HasPrefix(fields[1], "Z") {
		return lp
	}
	lp.pgid, _ = strconv.Atoi(fields[0])
	lp.startTicks = start
	lp.program = fields[2] // argv[0]
	lp.state = procAlive
	if !wantEnv {
		return lp
	}
	// Read the two markers from the process's environment. The environment is read from
	// KERN_PROCARGS2, which returns each variable as a separate NUL-terminated entry, so a
	// value that contains spaces or '=' stays intact — a run-dir path may hold either (a
	// spaced home, or a path component with an '='), and the reference reaps such an orphan
	// (VM-measured). A space-joined `ps -E` line could not tell those bytes from the next
	// environment entry.
	env := readProcEnv(pid)
	lp.runDir = envLookup(env, "CLAUDE_SSH_RUN_DIR=")
	lp.childMark = envLookup(env, "CLAUDE_SSH_CHILD=")
	return lp
}

// envLookup returns the value of the first environment entry with the given "KEY=" prefix, or
// "" when absent. Each entry is a whole KEY=VALUE string (from KERN_PROCARGS2's NUL-delimited
// layout), so the match is exact and the value is returned verbatim.
func envLookup(env []string, prefix string) string {
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return e[len(prefix):]
		}
	}
	return ""
}

// readProcEnv returns pid's environment as "KEY=VALUE" entries, read from KERN_PROCARGS2. A
// seam so tests drive the reader without the syscall.
var readProcEnv = realReadProcEnv

// realReadProcEnv reads pid's argument/environment block via sysctl KERN_PROCARGS2 and returns
// the environment entries. KERN_PROCARGS2 is the darwin way to read another process's
// environment without /proc; unlike `ps -E`, it keeps each entry separate (NUL-terminated), so
// values with spaces or '=' are unambiguous. It returns nil when the process is gone or the
// read fails (the caller then reads empty markers, which fail the reap's gates safely).
func realReadProcEnv(pid int) []string {
	// mib = { CTL_KERN, KERN_PROCARGS2, pid }.
	mib := []int32{1, 49, int32(pid)}
	var n uintptr
	// First call with a nil buffer asks for the size.
	if _, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		0, uintptr(unsafe.Pointer(&n)), 0, 0); errno != 0 || n == 0 {
		return nil
	}
	buf := make([]byte, n)
	if _, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0); errno != 0 {
		return nil
	}
	return parseProcArgs2Env(buf[:n])
}

// parseProcArgs2Env extracts the environment entries from a KERN_PROCARGS2 buffer. The layout
// is: a 4-byte argc, the executable path (NUL-terminated) with trailing NUL padding, then argc
// NUL-terminated argv strings, then the NUL-terminated environment strings to the end. It skips
// argc and the argv block and returns the environment entries.
func parseProcArgs2Env(buf []byte) []string {
	if len(buf) < 4 {
		return nil
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	if argc < 0 {
		return nil
	}
	p := 4
	// Skip the executable path and the NUL padding that follows it.
	for p < len(buf) && buf[p] != 0 {
		p++
	}
	for p < len(buf) && buf[p] == 0 {
		p++
	}
	// Skip argc argv strings.
	for i := 0; i < argc && p < len(buf); i++ {
		for p < len(buf) && buf[p] != 0 {
			p++
		}
		p++ // step past the terminating NUL
	}
	// The remainder is the NUL-terminated environment.
	var env []string
	for p < len(buf) {
		start := p
		for p < len(buf) && buf[p] != 0 {
			p++
		}
		if p > start {
			env = append(env, string(buf[start:p]))
		}
		p++ // step past the terminating NUL
	}
	return env
}
