//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
)

// Linux machine identity and holder verification for the run-dir lock, read from /proc.
// Matches the reference's Linux build: the node is the boot id joined to the pid
// namespace inode (nodeID, procinfo_linux.go), the holder's command line comes from /proc/<pid>/cmdline, and the
// pid-namespace guard compares /proc/<pid>/ns/pid to our own.

// pidNamespaceRefusal returns a reason to refuse when pid is not verifiably in our own
// pid namespace. A missing /proc/<pid> (ENOENT) means the holder already exited, which
// is safe to "signal" (the kill returns ESRCH and the caller treats it as gone).
func pidNamespaceRefusal(pid int) string {
	self, err := osReadlink("/proc/self/ns/pid")
	if err != nil {
		return "this process's pid namespace is unreadable"
	}
	other, err := osReadlink("/proc/" + strconv.Itoa(pid) + "/ns/pid")
	if err != nil {
		if os.IsNotExist(err) {
			return "" // holder already gone; the signal will ESRCH harmlessly
		}
		return "holder cannot be inspected"
	}
	if other != self {
		return "holder runs in another pid namespace"
	}
	return ""
}

// realIsServeCmdline reads /proc/<pid>/cmdline and requires our own executable basename
// as argv0, a -serve/--serve flag, and a -socket/--socket naming this socket (matched by
// cleaned path, see matchesServeArgv).
func realIsServeCmdline(pid int, socket string) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	argv := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	return matchesServeArgv(argv, socket, selfBase())
}
