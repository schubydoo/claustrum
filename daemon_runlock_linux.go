//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
)

// Linux machine identity and holder verification for the run-dir lock, read from /proc.
// Matches the reference's Linux build: the node is the boot id joined to the pid
// namespace inode, the holder's command line comes from /proc/<pid>/cmdline, and the
// pid-namespace guard compares /proc/<pid>/ns/pid to our own.

// readBootID and osReadlink are seams over the /proc reads below. On the hosts
// this suite runs on the boot id is readable and non-empty and the pid-namespace
// link resolves; a procfs mounted subset=pid (systemd ProcSubset=) hides the boot
// id — the case the "" arms exist for — but no fixture can remount /proc, so
// nodeID's three "" arms and pidNamespaceRefusal's unreadable-self arm are
// unreachable without stand-ins.
var (
	readBootID = func() ([]byte, error) { return os.ReadFile("/proc/sys/kernel/random/boot_id") }
	osReadlink = os.Readlink
)

// nodeID builds the machine-plus-pid-namespace identity the reference writes into the
// owner record and compares before signalling: the boot id joined to the pid-namespace
// inode with a single "/". It returns "" when either part is unavailable, and an empty
// node makes the signal guard refuse eviction.
func nodeID() string {
	boot, err := readBootID()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(boot))
	if b == "" {
		return ""
	}
	ns, err := osReadlink("/proc/self/ns/pid")
	if err != nil {
		return ""
	}
	return b + "/" + ns
}

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
