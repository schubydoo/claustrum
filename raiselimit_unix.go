//go:build !windows

package main

import "syscall"

// getRlimit and setRlimit are the RLIMIT_NOFILE syscalls held as variables so a
// test can inject a failure to exercise the read-error and fallback paths.
var (
	getRlimit = syscall.Getrlimit
	setRlimit = syscall.Setrlimit
)

// raiseInheritedFileLimit sets this process's RLIMIT_NOFILE soft limit to
// min(hard, 65536), keeping the hard limit, so process.spawn children inherit a high
// open-file limit (reference build 19f30c46). It reads the current limit, then sets
// the raised soft limit; on failure it falls back to the original limit. A
// Getrlimit failure or both Setrlimit attempts failing leaves the limit unchanged.
//
// The Setrlimit call runs even when the soft limit already equals the target: the
// call, not the value, is what updates the limit the Go runtime restores across
// exec, so a spawned child inherits the raised limit rather than the daemon's
// original startup soft limit.
//
// Only after a raise that works, it writes one INFO line to the daemon log with
// the limit it set. CLAUSTRUM_LOG_LEVEL=warn drops that line. f6010b97 writes the
// same `[daemon]` line, without a level tag, as the first line of its log. That
// is before the "listening" line. Linux and macOS VMs measured this, with the
// value 65536. The line was not seen on the
// Windows VM. A failed raise writes no line. That path was not measured.
func raiseInheritedFileLimit() {
	var lim syscall.Rlimit
	if err := getRlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return
	}
	target := min(uint64(65536), lim.Max)
	if err := setRlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: target, Max: lim.Max}); err != nil {
		_ = setRlimit(syscall.RLIMIT_NOFILE, &lim)
		return
	}
	logInfof("[daemon] child processes will start with an open-files limit of %d", target)
}
