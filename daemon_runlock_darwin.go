//go:build darwin

package main

import (
	"bytes"
	"encoding/binary"
	"strings"

	"golang.org/x/sys/unix"
)

// macOS machine identity and holder verification for the run-dir lock. macOS has no
// /proc, so the node comes from sysctl kern.bootsessionuuid and the holder's command
// line comes from sysctl KERN_PROCARGS2.
//
// The reference's macOS build does not verify the holder's command line before
// signalling it: it sends SIGTERM then SIGKILL to the recorded pid unverified (observed
// on a macOS VM — it signals even a pid that is not a serve process). claustrum instead
// verifies via KERN_PROCARGS2 and refuses to signal a pid that is not our serve process.
// That is an intentional hardening divergence — see docs/DIVERGENCES.md D15.

// nodeID returns the macOS machine identity: the per-boot session UUID from sysctl
// kern.bootsessionuuid, trimmed of a trailing NUL and surrounding whitespace. Unlike
// Linux there is no pid-namespace component (macOS has no pid namespaces). It returns ""
// on sysctl error, which makes the signal guard refuse eviction.
func nodeID() string {
	uuid, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.Trim(uuid, "\x00"))
}

// pidNamespaceRefusal is a no-op on macOS: there are no pid namespaces to compare, so
// this guard never refuses. The machine-identity (node) and command-line guards carry
// the verification instead.
func pidNamespaceRefusal(pid int) string { return "" }

// realIsServeCmdline reads pid's argument vector from sysctl KERN_PROCARGS2 and requires
// our own executable basename as argv0, a -serve/--serve flag, and a -socket/--socket
// naming exactly this socket — the same check the Linux path makes against
// /proc/<pid>/cmdline.
func realIsServeCmdline(pid int, socket string) bool {
	argv, ok := procArgv(pid)
	if !ok {
		return false
	}
	return matchesServeArgv(argv, socket, selfBase())
}

// procArgv returns pid's argument vector via sysctl KERN_PROCARGS2, or ok=false when it
// cannot be read (the process is gone, or the caller lacks permission). Without the argv
// realIsServeCmdline refuses, so an unreadable holder is never signalled.
func procArgv(pid int) ([]string, bool) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, false
	}
	return parseProcArgs2(raw), true
}

// parseProcArgs2 decodes a KERN_PROCARGS2 buffer: a little-endian int32 argc, the
// executable path (NUL-terminated), a run of NUL padding, then argc NUL-terminated
// argument strings (env follows and is ignored).
func parseProcArgs2(buf []byte) []string {
	if len(buf) < 4 {
		return nil
	}
	argc := int(int32(binary.LittleEndian.Uint32(buf[:4])))
	if argc <= 0 {
		return nil
	}
	p := buf[4:]
	// Skip the executable path up to and including its terminating NUL.
	if i := bytes.IndexByte(p, 0); i >= 0 {
		p = p[i+1:]
	} else {
		return nil
	}
	// Skip the NUL padding between the exec path and argv[0].
	for len(p) > 0 && p[0] == 0 {
		p = p[1:]
	}
	args := make([]string, 0, argc)
	for len(p) > 0 && len(args) < argc {
		i := bytes.IndexByte(p, 0)
		if i < 0 {
			args = append(args, string(p))
			break
		}
		args = append(args, string(p[:i]))
		p = p[i+1:]
	}
	return args
}
